.PHONY: test genpb

test:
	go test ./...

genpb:
	protoc --descriptor_set_out=testdata/contracts/flights.pb --include_imports testdata/contracts/flights.proto
