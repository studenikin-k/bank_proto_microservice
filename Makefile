.PHONY: proto clean

PROTO_DIR = proto

proto:
	protoc --proto_path=$(PROTO_DIR) \
		--go_out=$(PROTO_DIR) --go_opt=paths=source_relative \
		--go-grpc_out=$(PROTO_DIR) --go-grpc_opt=paths=source_relative \
		$(PROTO_DIR)/auth/*.proto \
		$(PROTO_DIR)/account/*.proto \
		$(PROTO_DIR)/transaction/*.proto

clean:
	rm -f $(PROTO_DIR)/*/*.pb.go