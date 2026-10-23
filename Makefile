TEST_DB_URL ?= postgres://postgres:pw@127.0.0.1:55432/postgres?sslmode=disable

.PHONY: test test-db db-up db-down lint build

test:
	go test ./...

db-up:
	@docker inspect rp-testdb >/dev/null 2>&1 || docker run -d --name rp-testdb -e POSTGRES_PASSWORD=pw -p 55432:5432 postgres:16 >/dev/null
	@until docker exec rp-testdb pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done

db-down:
	docker rm -f rp-testdb

test-db: db-up
	RP_TEST_DATABASE_URL='$(TEST_DB_URL)' go test -count=1 ./...

lint:
	go vet ./...

build:
	CGO_ENABLED=0 go build -o bin/router-peering ./cmd/router-peering
