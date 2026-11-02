package main

import "github.com/bolubo/checkpoint-governor/internal/notify"

// webhookPoster turns the `--webhook` flag into the loop's hook. Nil for an empty flag: a
// deployment with no receiver must not carry a function whose only behaviour is to fail.
func webhookPoster(url string) func(notify.Event) error {
	if url == "" {
		return nil
	}
	return notify.New(url).Post
}
