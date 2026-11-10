package main

import "lanflow/internal/app"

// newService wires the real service (internal/app). main falls back to the
// fake backend, with a log line, when it cannot be created. lang is the
// language of a first run ("zh" or "en").
func newService(lang string) (app.API, error) {
	o := app.DefaultOptions("Lanflow")
	o.Language = lang
	svc, err := app.NewService(o)
	if err != nil {
		return nil, err
	}
	return svc, nil
}
