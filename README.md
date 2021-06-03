# simple-http-server-go

Simple http server with logging, tracing in go

> This go application is for a dummy http server which expose following endpoint

- `/` returns version information of application with a message.
- `/health` for health checks
- `/add?access_key=` post method for adding message to database
- `/messages` for getting message from database
