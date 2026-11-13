.PHONY: test test-integration lint seed

test:  ## unit tests: the manifest, the OpenAPI document and the routes, merge safety, the callbacks' crypto
	go vet ./... && go test ./...

test-integration:  ## L2 against PostgreSQL and the bus (BE_SDK_TEST_PG_ADMIN, BE_SDK_TEST_NATS_URL)
	go test -count=1 -tags integration ./itest/

lint:
	brickkit lint

seed:  ## the grants of the bindings (SEED_PASSWORD; see cmd/seed)
	go run ./cmd/seed
