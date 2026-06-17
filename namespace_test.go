package grpc

import "testing"

func TestServiceNamespace(t *testing.T) {
	if ServiceNamespace == "" {
		t.Fatal("ServiceNamespace must not be empty")
	}
}
