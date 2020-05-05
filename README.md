# Via - Simple generic HTTP server for messages and storage

This is a minimal but generic server that does two things:

-	pass messages between clients
-	store data

## Usage

Start the server:

	via [-v] [-d storage_dir] [port]

Then start sending requests on the client:

	# start listening for server sent event stream
	curl http://localhost:8001/msg/someid

	# POST a message
	curl http://localhost:8001/msg/someid -d somedata

	# Store, get, and delete a document
	curl http://localhost:8001/store/someid -d someid
	curl http://localhost:8001/store/someid
	curl http://localhost:8001/store/someid -X DELETE

You can also protect your message ID with a password so no one else can listen
to it at the same time:

	curl http://localhost:8001/msg/someid:somepassword
	curl http://localhost:8001/msg/someid  # 403
	curl http://localhost:8001/msg/someid -d somedata  # 200

You should regularly clean up old files:

	find {storage_dir} -type f -mtime +7 -delete
