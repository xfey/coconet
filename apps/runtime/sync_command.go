package main

import "context"

// Previous clients may still have queued this private entry point. Never send
// their captured sessions through the retired server ingestion API.
func runSync(context.Context, []string) int { return 2 }
