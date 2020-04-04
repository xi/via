# Via - Simple pubsub server

This is very much inspired by <https://patchbay.pub/> and its clones
[conduit](https://github.com/prologic/conduit) and
[duct](https://github.com/schollz/duct).


## Usage

Start the server:

	via [-v] [[host]:port]

Then start sending requests on the client:

	# initiate GET and wait for data
	curl http://localhost:8001/someid

	# start listening for server sent event stream
	curl http://localhost:8001/someid?sse

	# POST some data
	curl http://localhost:8001/someid -d somedata

You can also protect your ID with a password so no one else can listen to
it at the same time:

	curl http://localhost:8001/someid:somepassword
	curl http://localhost:8001/someid  # 403
	curl http://localhost:8001/someid -d somedata  # 200


## Differences to patchbay

-	no support for MPMC (blocking POST)
-	no support for req/res
-	support for [server-sent events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events)
-	support for passwords
