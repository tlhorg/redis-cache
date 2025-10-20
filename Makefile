vulncheck:
	govulncheck -show verbose ./...

lint:
	golangci-lint run

clean:
	go mod tidy
	go clean -cache
	go clean -testcache

upgrade:
	go get -u all

test:
	go test -v ./...