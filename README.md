# Goreman

Clone of [foreman](https://github.com/ddollar/foreman) written in golang.

1. Install `go install github.com/mattn/goreman@latest`
## Getting Started

    goreman start

Will start all commands defined in the `Procfile` and display their outputs.
Any signals are forwarded to each process.

```Procfile
export MINIO_ROOT_USER=minio
export MINIO_ROOT_PASSWORD=miniominio

web1: plackup --port $PORT
web2: plackup --port $PORT
web3: bundle exec ruby web.rb
web4: go run web.go -a :$PORT
```

## Example

See [`_example`](_example/) directory

## Design

The main goroutine loads `Procfile` and starts each command in the file. Afterwards, it is driven by the following two kinds of events, and then take proper action against the managed processes.

1. It receives a signal, which could be one of `SIGINT`, `SIGTERM`, and `SIGHUP`;
2. It receives an RPC call, which is triggered by the command `goreman run COMMAND [PROCESS...]`.

![design](images/design.png)

## Authors

Yasuhiro Matsumoto (a.k.a mattn)
