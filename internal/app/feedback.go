package app

import (
	"sync"

	"lanflow/internal/model"
)

// feedbackState is the state of 反馈问题 (docs/feedback-api.md).
type feedbackState struct {
	mu        sync.Mutex
	lastCrash *model.ErrorRecord // the previous run's crash, until answered
}

// kickAutoReport is called after every new error record.
func (s *Service) kickAutoReport() {}
