package tasks

import (
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/platform/contract"
	"github.com/slipgraph/slipgraph/internal/policy"
	"net/http"
	"time"
)

type HTTP struct {
	Store    *Store
	Identity *auth.HTTP
}

func parseID(w http.ResponseWriter, value string) (uuid.UUID, bool) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		auth.Error(w, auth.ErrInvalid)
		return uuid.Nil, false
	}
	return id, true
}
func inputTask(title, description, status string, owner *string, start, due time.Time, estimate int64, milestone bool) (TaskInput, error) {
	result := TaskInput{Title: title, Description: description, Status: status, Start: start, Due: due, EstimateMinutes: estimate, Milestone: milestone}
	if owner != nil {
		parsed, err := uuid.Parse(*owner)
		if err != nil || parsed == uuid.Nil {
			return result, auth.ErrInvalid
		}
		result.OwnerID = &parsed
	}
	return result, nil
}
func (h *HTTP) ListProjects(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Identity.Require(w, r, policy.ProjectRead)
	if !ok {
		return
	}
	result, err := h.Store.ListProjects(r.Context(), session)
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, result)
}
func (h *HTTP) CreateProject(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Identity.Require(w, r, policy.ProjectWrite)
	if !ok {
		return
	}
	var body contract.ProjectInput
	if err := auth.Decode(w, r, &body); err != nil {
		auth.Error(w, err)
		return
	}
	result, err := h.Store.CreateProject(r.Context(), session, ProjectInput{Name: body.Name, Description: body.Description, Start: body.Start, LaunchDue: body.LaunchDue})
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, result)
}
func (h *HTTP) GetProject(w http.ResponseWriter, r *http.Request, projectID string) {
	session, ok := h.Identity.Require(w, r, policy.ProjectRead)
	if !ok {
		return
	}
	id, ok := parseID(w, projectID)
	if !ok {
		return
	}
	result, err := h.Store.Board(r.Context(), session, id)
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, result)
}
func (h *HTTP) UpdateProject(w http.ResponseWriter, r *http.Request, projectID string) {
	session, ok := h.Identity.Require(w, r, policy.ProjectWrite)
	if !ok {
		return
	}
	id, ok := parseID(w, projectID)
	if !ok {
		return
	}
	var body contract.ProjectUpdateInput
	if err := auth.Decode(w, r, &body); err != nil {
		auth.Error(w, err)
		return
	}
	result, err := h.Store.UpdateProject(r.Context(), session, id, body.Version, ProjectInput{Name: body.Name, Description: body.Description, Start: body.Start, LaunchDue: body.LaunchDue})
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, result)
}
func (h *HTTP) DeleteProject(w http.ResponseWriter, r *http.Request, projectID string) {
	session, ok := h.Identity.Require(w, r, policy.ProjectWrite)
	if !ok {
		return
	}
	id, ok := parseID(w, projectID)
	if !ok {
		return
	}
	var body contract.VersionInput
	if err := auth.Decode(w, r, &body); err != nil {
		auth.Error(w, err)
		return
	}
	if err := h.Store.DeleteProject(r.Context(), session, id, body.Version); err != nil {
		auth.Error(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *HTTP) CreateTask(w http.ResponseWriter, r *http.Request, projectID string) {
	session, ok := h.Identity.Require(w, r, policy.TaskWrite)
	if !ok {
		return
	}
	id, ok := parseID(w, projectID)
	if !ok {
		return
	}
	var body contract.TaskInput
	if err := auth.Decode(w, r, &body); err != nil {
		auth.Error(w, err)
		return
	}
	input, err := inputTask(body.Title, body.Description, string(body.Status), body.OwnerId, body.Start, body.Due, body.EstimateMinutes, body.Milestone)
	if err != nil {
		auth.Error(w, err)
		return
	}
	result, err := h.Store.CreateTask(r.Context(), session, id, input)
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, result)
}
func (h *HTTP) GetTask(w http.ResponseWriter, r *http.Request, taskID string) {
	session, ok := h.Identity.Require(w, r, policy.TaskRead)
	if !ok {
		return
	}
	id, ok := parseID(w, taskID)
	if !ok {
		return
	}
	result, err := h.Store.GetTask(r.Context(), session, id)
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, result)
}
func (h *HTTP) UpdateTask(w http.ResponseWriter, r *http.Request, taskID string) {
	session, ok := h.Identity.Require(w, r, policy.TaskWrite)
	if !ok {
		return
	}
	id, ok := parseID(w, taskID)
	if !ok {
		return
	}
	var body contract.TaskUpdateInput
	if err := auth.Decode(w, r, &body); err != nil {
		auth.Error(w, err)
		return
	}
	input, err := inputTask(body.Title, body.Description, string(body.Status), body.OwnerId, body.Start, body.Due, body.EstimateMinutes, body.Milestone)
	if err != nil {
		auth.Error(w, err)
		return
	}
	result, err := h.Store.UpdateTask(r.Context(), session, id, body.Version, input)
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, result)
}
func (h *HTTP) DeleteTask(w http.ResponseWriter, r *http.Request, taskID string) {
	session, ok := h.Identity.Require(w, r, policy.TaskWrite)
	if !ok {
		return
	}
	id, ok := parseID(w, taskID)
	if !ok {
		return
	}
	var body contract.VersionInput
	if err := auth.Decode(w, r, &body); err != nil {
		auth.Error(w, err)
		return
	}
	if err := h.Store.DeleteTask(r.Context(), session, id, body.Version); err != nil {
		auth.Error(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *HTTP) ReportDelay(w http.ResponseWriter, r *http.Request, taskID string) {
	session, ok := h.Identity.Require(w, r, policy.TaskWrite)
	if !ok {
		return
	}
	id, ok := parseID(w, taskID)
	if !ok {
		return
	}
	var body contract.DelayInput
	if err := auth.Decode(w, r, &body); err != nil {
		auth.Error(w, err)
		return
	}
	result, err := h.Store.ReportDelay(r.Context(), session, id, body.Version, body.Days)
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, result)
}
func (h *HTTP) AddDependency(w http.ResponseWriter, r *http.Request, taskID string) {
	session, ok := h.Identity.Require(w, r, policy.TaskWrite)
	if !ok {
		return
	}
	id, ok := parseID(w, taskID)
	if !ok {
		return
	}
	var body contract.DependencyInput
	if err := auth.Decode(w, r, &body); err != nil {
		auth.Error(w, err)
		return
	}
	blocker, ok := parseID(w, body.BlockerId)
	if !ok {
		return
	}
	result, err := h.Store.AddDependency(r.Context(), session, id, blocker, body.Version)
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, result)
}
func (h *HTTP) RemoveDependency(w http.ResponseWriter, r *http.Request, taskID, blockerID string) {
	session, ok := h.Identity.Require(w, r, policy.TaskWrite)
	if !ok {
		return
	}
	id, ok := parseID(w, taskID)
	if !ok {
		return
	}
	blocker, ok := parseID(w, blockerID)
	if !ok {
		return
	}
	var body contract.VersionInput
	if err := auth.Decode(w, r, &body); err != nil {
		auth.Error(w, err)
		return
	}
	result, err := h.Store.RemoveDependency(r.Context(), session, id, blocker, body.Version)
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, result)
}
func (h *HTTP) GetRisk(w http.ResponseWriter, r *http.Request, projectID string) {
	session, ok := h.Identity.Require(w, r, policy.RiskRead)
	if !ok {
		return
	}
	id, ok := parseID(w, projectID)
	if !ok {
		return
	}
	result, err := h.Store.Risk(r.Context(), session, id)
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, result)
}
