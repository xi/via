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

type Topic struct {
	sync.Mutex
	channels   map[chan Msg]bool
	hasHistory bool
	history    []Msg
	path       string
	lastId     int
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

func (topic *Topic) post(data []byte) {
	topic.lastId += 1
	msg := Msg{topic.lastId, data}

	if topic.hasHistory {
		topic.history = append(topic.history, msg)

		for len(topic.history) > maxHistorySize {
			topic.history = topic.history[1:]
		}
	}

	for ch := range topic.channels {
		ch <- msg
	}
}

func (topic *Topic) put(data []byte, lastId int) {
	if len(topic.history) > 0 && lastId < topic.history[0].Id {
		return
	}

	history := make([]Msg, 0)
	history = append(history, Msg{lastId, data})
	for _, msg := range topic.history {
		if msg.Id > lastId {
			history = append(history, msg)
		}
	}
	topic.history = history

	if lastId > topic.lastId {
		topic.lastId = lastId
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
		}
		if topic.hasHistory {
			topic.restoreHistory()
		}
		topics[key] = topic
	}

	return topic
}

func pushChannel(key string, ch chan Msg, lastId int) {
	topic := getTopic(key)

	go func() {
		topic.Lock()
		defer topic.Unlock()

		for _, msg := range topic.history {
			if msg.Id > lastId {
				ch <- msg
			}
		}

		topic.channels[ch] = true
	}()
}

func popChannel(key string, ch chan Msg) {
	mux.Lock()
	defer mux.Unlock()
	topic := topics[key]

	topic.Lock()
	delete(topic.channels, ch)
	topic.Unlock()

	if len(topic.channels) == 0 {
		if verbose {
			log.Println("clearing topic", key)
		}
		delete(topics, key)
	}
}

func get(w http.ResponseWriter, r *http.Request) {
	lastId, err := strconv.Atoi(r.Header.Get("Last-Event-ID"))
	if err != nil {
		lastId = 0
	}

	ch := make(chan Msg)
	pushChannel(r.URL.Path, ch, lastId)
	defer popChannel(r.URL.Path, ch)

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

	topic := getTopic(r.URL.Path)

	response := make(map[string]int)
	defer func() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}()

	topic.Lock()
	defer topic.Unlock()

	topic.post(body)

	if topic.hasHistory {
		topic.storeHistory()
		response["historyRemaining"] = maxHistorySize - len(topic.history)
	}
}

func put(w http.ResponseWriter, r *http.Request) {
	if !hasHistory(r.URL.Path) {
		http.Error(w, "No history", http.StatusBadRequest)
		return
	}

	topic := getTopic(r.URL.Path)

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

	topic.Lock()
	defer topic.Unlock()

	topic.put(body, lastId)
	topic.storeHistory()
}

func del(w http.ResponseWriter, r *http.Request) {
	if !hasHistory(r.URL.Path) {
		http.Error(w, "No history", http.StatusBadRequest)
		return
	}

	topic := getTopic(r.URL.Path)

	topic.Lock()
	defer topic.Unlock()

	topic.history = make([]Msg, 0)
	topic.lastId = 0
	topic.deleteHistory()
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
