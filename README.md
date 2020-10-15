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

You can also protect your ID with a password so no one else can listen to
it at the same time:

	curl http://localhost:8001/msg/someid:somepassword
	curl http://localhost:8001/msg/someid  # 403
	curl http://localhost:8001/msg/someid -d somedata  # 200

You should regularly clean up old files:

	find {storage_dir} -type f -mtime +7 -delete

## Differences to patchbay

-	no support for MPMC (blocking POST)
-	no support for req/res
-	no support for blocking GET
-	support for [server-sent events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events)
-	support for passwords
