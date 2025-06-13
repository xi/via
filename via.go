package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Msg struct {
	Id   int
	Data []byte
}

type Sub struct {
	ch     chan Msg
	lastId int
}

type Post struct {
	data []byte
	ch   chan int
}

type Topic struct {
	channels   map[chan Msg]bool
	hasHistory bool
	history    []Msg
	path       string
	lastId     int
	subChan    chan Sub
	unsubChan  chan chan Msg
	postChan   chan Post
	putChan    chan Msg
	delChan    chan struct{}
}

var mux = &sync.Mutex{}
var topics = make(map[string]*Topic)
var verbose = false
var maxHistorySize = 100
var dir = ""

func hasHistory(key string) bool {
	return strings.HasPrefix(key, "/hmsg/")
}

func (topic *Topic) storeHistory() {
	content, err := json.Marshal(topic.history)
	if err != nil {
		log.Println("error storing history:", err)
		return
	}

	err = os.WriteFile(topic.path, content, 0644)
	if err != nil {
		log.Println("error storing history:", err)
		return
	}
}

func (topic *Topic) restoreHistory() {
	content, err := os.ReadFile(topic.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Println("error restoring history:", err)
		}
		return
	}

	var history []Msg
	err = json.Unmarshal(content, &history)
	if err != nil {
		log.Println("error restoring history:", err)
		return
	}

	topic.history = history
	if len(history) > 0 {
		topic.lastId = history[len(history)-1].Id
	}
}

func (topic *Topic) deleteHistory() {
	err := os.Remove(topic.path)
	if err != nil && !os.IsNotExist(err) {
		log.Println("error deleting history:", err)
	}
}

func (topic *Topic) cleanup(key string) bool {
	if len(topic.channels) > 0 {
		return false
	}

	if verbose {
		log.Println("clearing topic", key)
	}
	mux.Lock()
	delete(topics, key)
	mux.Unlock()
	return true
}

func (topic *Topic) run(key string) {
	if topic.hasHistory {
		topic.restoreHistory()
	}

	for {
		select {
		case sub := <-topic.subChan:
			for _, msg := range topic.history {
				if msg.Id > sub.lastId {
					sub.ch <- msg
				}
			}

			topic.channels[sub.ch] = true
		case ch := <-topic.unsubChan:
			close(ch)
			delete(topic.channels, ch)
		case post := <-topic.postChan:
			topic.lastId += 1
			msg := Msg{topic.lastId, post.data}

			if topic.hasHistory {
				topic.history = append(topic.history, msg)
				for len(topic.history) > maxHistorySize {
					topic.history = topic.history[1:]
				}
				topic.storeHistory()

				post.ch <- maxHistorySize - len(topic.history)
			}

			close(post.ch)

			for ch := range topic.channels {
				ch <- msg
			}
		case msg := <-topic.putChan:
			if len(topic.history) > 0 && msg.Id < topic.history[0].Id {
				continue
			}

			history := make([]Msg, 0)
			history = append(history, msg)
			for _, m := range topic.history {
				if m.Id > msg.Id {
					history = append(history, m)
				}
			}
			topic.history = history
			topic.storeHistory()

			if msg.Id > topic.lastId {
				topic.lastId = msg.Id
			}
		case _ = <-topic.delChan:
			topic.history = make([]Msg, 0)
			topic.lastId = 0
			topic.deleteHistory()
		}

		if topic.cleanup(key) {
			break
		}
	}
}

func getTopic(key string) *Topic {
	mux.Lock()
	defer mux.Unlock()
	topic, exists := topics[key]

	if !exists {
		filename := base64.URLEncoding.EncodeToString([]byte(key))
		topic = &Topic{
			channels:   make(map[chan Msg]bool, 0),
			hasHistory: hasHistory(key),
			history:    make([]Msg, 0),
			path:       path.Join(dir, filename),
			lastId:     0,
			subChan:    make(chan Sub),
			unsubChan:  make(chan chan Msg),
			postChan:   make(chan Post),
			putChan:    make(chan Msg),
			delChan:    make(chan struct{}),
		}
		topics[key] = topic
		go topic.run(key)
	}

	return topic
}

func get(w http.ResponseWriter, r *http.Request) {
	lastId, err := strconv.Atoi(r.Header.Get("Last-Event-ID"))
	if err != nil {
		lastId = 0
	}

	topic := getTopic(r.URL.Path)

	ch := make(chan Msg)
	topic.subChan <- Sub{ch, lastId}

	ctx := r.Context()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, ": ping\n\n")
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			log.Println("lost a connection on", r.URL.Path)
			go func() {
				topic.unsubChan <- ch
			}()
			for _ = range ch {
				// drain channel until unusub closes it
			}
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case msg := <-ch:
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", msg.Id, msg.Data)
			flusher.Flush()
		}
	}
}

func post(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Println("error reading request body:", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	ch := make(chan int)
	topic := getTopic(r.URL.Path)
	topic.postChan <- Post{body, ch}

	response := make(map[string]int)
	remaining, ok := <-ch
	if ok {
		response["historyRemaining"] = remaining
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func put(w http.ResponseWriter, r *http.Request) {
	if !hasHistory(r.URL.Path) {
		http.Error(w, "No history", http.StatusBadRequest)
		return
	}

	lastId, err := strconv.Atoi(r.Header.Get("Last-Event-ID"))
	if err != nil {
		http.Error(w, "Missing Last-Event-ID", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Println("error reading request body:", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	topic := getTopic(r.URL.Path)
	topic.putChan <- Msg{lastId, body}
}

func del(w http.ResponseWriter, r *http.Request) {
	if !hasHistory(r.URL.Path) {
		http.Error(w, "No history", http.StatusBadRequest)
		return
	}

	topic := getTopic(r.URL.Path)
	topic.delChan <- struct{}{}
}

func handler(w http.ResponseWriter, r *http.Request) {
	if verbose {
		log.Println(r.Method, r.URL)
	}

	if r.Method == http.MethodGet {
		get(w, r)
	} else if r.Method == http.MethodPost {
		post(w, r)
	} else if r.Method == http.MethodPut {
		put(w, r)
	} else if r.Method == http.MethodDelete {
		del(w, r)
	} else {
		http.Error(w, "Unsupported Method", http.StatusMethodNotAllowed)
	}
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "via [-v] [-d storage_dir] [port]\n")
		flag.PrintDefaults()
	}

	flag.BoolVar(&verbose, "v", false, "enable verbose logs")
	flag.StringVar(&dir, "d", ".", "directory for storage")
	flag.Parse()

	addr := "localhost:8001"
	if len(flag.Args()) > 0 {
		addr = fmt.Sprintf("localhost:%s", flag.Args()[0])
	}

	http.HandleFunc("/msg/", handler)
	http.HandleFunc("/hmsg/", handler)

	ctx, unregisterSignals := signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	ctxFactory := func(l net.Listener) context.Context { return ctx }
	server := &http.Server{Addr: addr, BaseContext: ctxFactory}

	go func() {
		log.Printf("Serving on http://%s", addr)
		err := server.ListenAndServe()
		if err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	unregisterSignals()
	log.Println("Shutting down server…")
	server.Shutdown(context.Background())
}
