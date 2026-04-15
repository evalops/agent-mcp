package agentmcp

import (
	"slices"
	"strings"
	"sync"

	approvalsv1 "github.com/evalops/proto/gen/go/approvals/v1"
)

type ApprovalHabitsCache struct {
	mu         sync.RWMutex
	workspaces map[string][]*approvalsv1.ApprovalHabit
}

func NewApprovalHabitsCache() *ApprovalHabitsCache {
	return &ApprovalHabitsCache{
		workspaces: make(map[string][]*approvalsv1.ApprovalHabit),
	}
}

func (cache *ApprovalHabitsCache) Get(workspaceID string) ([]*approvalsv1.ApprovalHabit, bool) {
	if cache == nil {
		return nil, false
	}
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	habits, ok := cache.workspaces[strings.TrimSpace(workspaceID)]
	if !ok {
		return nil, false
	}
	return cloneApprovalHabits(habits), true
}

func (cache *ApprovalHabitsCache) Store(workspaceID string, habits []*approvalsv1.ApprovalHabit) {
	if cache == nil {
		return
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.workspaces[workspaceID] = normalizeApprovalHabits(habits)
}

func (cache *ApprovalHabitsCache) Upsert(workspaceID string, habit *approvalsv1.ApprovalHabit) bool {
	if cache == nil || habit == nil {
		return false
	}
	workspaceID = strings.TrimSpace(workspaceID)
	pattern := strings.TrimSpace(habit.GetPattern())
	if workspaceID == "" || pattern == "" {
		return false
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	habits, ok := cache.workspaces[workspaceID]
	if !ok {
		return false
	}
	cloned := cloneApprovalHabit(habit)
	for index, current := range habits {
		if strings.TrimSpace(current.GetPattern()) == pattern {
			habits[index] = cloned
			cache.workspaces[workspaceID] = normalizeApprovalHabits(habits)
			return true
		}
	}
	habits = append(habits, cloned)
	cache.workspaces[workspaceID] = normalizeApprovalHabits(habits)
	return true
}

func normalizeApprovalHabits(habits []*approvalsv1.ApprovalHabit) []*approvalsv1.ApprovalHabit {
	normalized := cloneApprovalHabits(habits)
	slices.SortFunc(normalized, func(left, right *approvalsv1.ApprovalHabit) int {
		return strings.Compare(strings.TrimSpace(left.GetPattern()), strings.TrimSpace(right.GetPattern()))
	})
	return normalized
}

func cloneApprovalHabits(habits []*approvalsv1.ApprovalHabit) []*approvalsv1.ApprovalHabit {
	cloned := make([]*approvalsv1.ApprovalHabit, 0, len(habits))
	for _, habit := range habits {
		if habit == nil {
			continue
		}
		cloned = append(cloned, cloneApprovalHabit(habit))
	}
	return cloned
}

func cloneApprovalHabit(habit *approvalsv1.ApprovalHabit) *approvalsv1.ApprovalHabit {
	if habit == nil {
		return nil
	}
	return &approvalsv1.ApprovalHabit{
		Pattern:               habit.GetPattern(),
		AutoApproveConfidence: habit.GetAutoApproveConfidence(),
		ObservationCount:      habit.GetObservationCount(),
		ApprovedCount:         habit.GetApprovedCount(),
	}
}
