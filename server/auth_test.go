package server

import (
	"context"
	"net/http"
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestRequestFromMetadataCookie(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"cookie", "access_token=jwt-token",
	))
	req, err := requestFromMetadata(ctx, "Authorization", "access_token")
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := req.Cookie("access_token")
	if err != nil {
		t.Fatal(err)
	}
	if cookie.Value != "jwt-token" {
		t.Fatalf("unexpected cookie value: %q", cookie.Value)
	}
}

func TestRequestFromMetadataCookieKeyMetadata(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"session", "cookie-jwt",
	))
	req, err := requestFromMetadata(ctx, "Authorization", "session")
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := req.Cookie("session")
	if err != nil {
		t.Fatal(err)
	}
	if cookie.Value != "cookie-jwt" {
		t.Fatalf("unexpected cookie value: %q", cookie.Value)
	}
}

func TestHasAuthCookie(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "access_token", Value: "x"})
	if !hasAuthCookie(req, "access_token") {
		t.Fatal("expected auth cookie to be detected")
	}
}
