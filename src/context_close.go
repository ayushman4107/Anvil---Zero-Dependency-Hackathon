package main

import (
	"context"
	"io"
)

func closeOnContextDone(ctx context.Context, closer io.Closer) func() {
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(callbackDone)
		_ = closer.Close()
	})
	return func() {
		if !stop() {
			<-callbackDone
		}
	}
}
