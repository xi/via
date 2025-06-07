# Via - Simple pubsub server

This is very much inspired by <https://patchbay.pub/> and its clones
[conduit](https://github.com/prologic/conduit) and
[duct](https://github.com/schollz/duct).

## Usage

Start the server:

	via [-v] [-d storage_dir] [port]

Then start sending requests on the client:

	# start listening for server sent event stream
	curl http://localhost:8001/msg/someid

	# POST a message
	curl http://localhost:8001/msg/someid -d somedata

Use the `hmsg` prefix if you want to keep a history:

	# start listening and request any messages you may have missed
	curl http://localhost:8001/hmsg/someid -H 'Last-Event-Id: 3'

	# POST works just as before
	curl http://localhost:8001/hmsg/someid -d somedata

	# DELETE deletes the history
	curl http://localhost:8001/hmsg/someid -X DELETE

	# the history only keeps up to 100 entries
	# you can consolidate it by replacing all entries by a single message
	# the `historyRemaining` field in POST responses tells you how much space is left
	curl http://localhost:8001/hmsg/someid -d combined -H 'Last-Event-Id: 3' -X PUT

You should regularly clean up old files:

	find {storage_dir} -type f -mtime +7 -delete

## Differences to patchbay

-	no support for MPMC (blocking POST)
-	no support for req/res
-	no support for blocking GET
-	support for [server-sent events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events)
-	support for passwords
-	support for message history
