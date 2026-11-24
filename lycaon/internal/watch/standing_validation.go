package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	awaitstore "github.com/lycaon/lycaon/internal/await"
)

type preparedStandingInput struct {
	ProjectID        string
	Name             string
	Prompt           string
	Conditions       []awaitstore.Condition
	Gate             *awaitstore.Condition
	MinGapMinutes    int
	Target           StandingTarget
	WorkflowID       string
	ModelID          string
	RunSpendLimitUSD float64
	AllowUnpriced    bool
	DailyRunCap      int
	TimeZone         string
	Authority        Authority
}

func normalizeStandingInput(ctx context.Context, e *StandingEngine, in StandingWatchInput, now time.Time) (preparedStandingInput, error) {
	projectID := strings.TrimSpace(in.ProjectID)
	if projectID == "" {
		return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "project_id", Message: "project_id is required"}
	}
	name := strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
		return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "name", Message: "name must be 1 to 80 characters"}
	}
	prompt := strings.TrimSpace(in.Prompt)
	if n := utf8.RuneCountInString(prompt); n < 1 || n > 8000 {
		return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "prompt", Message: "prompt must be 1 to 8000 characters"}
	}
	if len(in.Conditions) < 1 || len(in.Conditions) > MaxAlternatives {
		return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "conditions", Message: "conditions must contain 1 to 8 items"}
	}

	defWorkflow, defModel, defTZ, defAuth := lookupProjectDefaults(ctx, e, projectID)
	auth := in.Authority
	if len(auth.Roots) == 0 && len(defAuth.Roots) > 0 {
		auth = defAuth
	}
	tz := strings.TrimSpace(in.TimeZone)
	if tz == "" {
		tz = defTZ
	}
	if tz == "" || strings.EqualFold(tz, "Local") {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "time_zone", Message: "time_zone must be a valid IANA zone"}
	}

	allTimers := true
	normConds := make([]awaitstore.Condition, len(in.Conditions))
	for i, c := range in.Conditions {
		nc, cErr := validateStandingCondition(fmt.Sprintf("conditions[%d]", i), c, auth, tz, loc, now)
		if cErr != nil {
			return preparedStandingInput{}, cErr
		}
		if nc.Kind != "timer" {
			allTimers = false
		}
		normConds[i] = nc
	}

	var normGate *awaitstore.Condition
	if in.Gate != nil {
		if !allTimers {
			return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "gate", Message: "gate is allowed only when every condition is a timer"}
		}
		if in.Gate.Kind != "git_ref" && in.Gate.Kind != "file_change" {
			return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "gate.kind", Message: "gate must be git_ref or file_change"}
		}
		gc, gErr := validateStandingCondition("gate", *in.Gate, auth, tz, loc, now)
		if gErr != nil {
			return preparedStandingInput{}, gErr
		}
		normGate = &gc
	}

	minGap := DefaultMinGapMinutes
	if in.MinGapMinutes != nil {
		minGap = *in.MinGapMinutes
		if minGap < MinStandingGapMinutes || minGap > MaxStandingGapMinutes {
			return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "min_gap_minutes", Message: "min_gap_minutes must be between 15 and 1440"}
		}
	}

	target := StandingTarget{Kind: "new_chat"}
	if in.Target != nil && strings.TrimSpace(in.Target.Kind) != "" {
		target.Kind = strings.TrimSpace(in.Target.Kind)
		target.SessionID = strings.TrimSpace(in.Target.SessionID)
	}
	switch target.Kind {
	case "new_chat":
		if target.SessionID != "" {
			return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "target.session_id", Message: "new_chat target must not set session_id"}
		}
	case "existing_chat":
		if target.SessionID == "" {
			return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "target.session_id", Message: "existing_chat target requires session_id"}
		}
		exists, _, tErr := checkSessionTarget(ctx, e, projectID, target.SessionID)
		if tErr != nil {
			return preparedStandingInput{}, tErr
		}
		if !exists {
			return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "target.session_id", Message: "existing_chat session must belong to the same project"}
		}
	default:
		return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "target.kind", Message: "target.kind must be new_chat or existing_chat"}
	}

	workflowID := strings.TrimSpace(in.WorkflowID)
	if workflowID == "" {
		workflowID = defWorkflow
	}
	modelID := strings.TrimSpace(in.ModelID)
	if modelID == "" {
		modelID = defModel
	}

	spendLimit := DefaultRunSpendLimitUSD
	if in.RunSpendLimitUSD != nil {
		spendLimit = math.Round(*in.RunSpendLimitUSD*100) / 100
		if spendLimit < MinRunSpendLimitUSD || spendLimit > MaxRunSpendLimitUSD {
			return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "run_spend_limit_usd", Message: "run_spend_limit_usd must be between 0.10 and 100.00"}
		}
	}
	allowUnpriced := false
	if in.AllowUnpriced != nil {
		allowUnpriced = *in.AllowUnpriced
	}
	if !allowUnpriced {
		priced, pErr := checkModelPriced(ctx, e, projectID, modelID)
		if pErr != nil {
			return preparedStandingInput{}, pErr
		}
		if !priced {
			return preparedStandingInput{}, &SpendUnenforceableError{ModelID: modelID}
		}
	}

	dailyCap := DefaultDailyRunCap
	if in.DailyRunCap != nil {
		dailyCap = *in.DailyRunCap
		if dailyCap < MinDailyRunCap || dailyCap > MaxDailyRunCap {
			return preparedStandingInput{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "daily_run_cap", Message: "daily_run_cap must be between 1 and 96"}
		}
	}

	return preparedStandingInput{
		ProjectID: projectID, Name: name, Prompt: prompt, Conditions: normConds, Gate: normGate,
		MinGapMinutes: minGap, Target: target, WorkflowID: workflowID, ModelID: modelID,
		RunSpendLimitUSD: spendLimit, AllowUnpriced: allowUnpriced, DailyRunCap: dailyCap,
		TimeZone: tz, Authority: auth,
	}, nil
}

func validateStandingCondition(prefix string, c awaitstore.Condition, auth Authority, defaultTZ string, defaultLoc *time.Location, now time.Time) (awaitstore.Condition, error) {
	switch strings.TrimSpace(c.Kind) {
	case "timer":
		if c.DelayMS != 0 {
			return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix + ".delay_ms", Message: "standing watch timers use every_minutes, at, or calendar"}
		}
		modes := boolToInt(c.EveryMinutes != 0) + boolToInt(strings.TrimSpace(c.At) != "") + boolToInt(c.Calendar != nil)
		if modes != 1 {
			return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix, Message: "standing watch timer requires exactly one of every_minutes, at, or calendar"}
		}
		out := awaitstore.Condition{Kind: "timer"}
		if c.EveryMinutes != 0 {
			if c.EveryMinutes < MinEveryMinutes || c.EveryMinutes > MaxEveryMinutes {
				return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix + ".every_minutes", Message: "every_minutes must be between 15 and 10080"}
			}
			out.EveryMinutes = c.EveryMinutes
			return out, nil
		}
		tz, loc := strings.TrimSpace(c.TimeZone), defaultLoc
		if tz != "" {
			loaded, err := time.LoadLocation(tz)
			if err != nil {
				return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix + ".time_zone", Message: "time_zone must be a valid IANA zone"}
			}
			loc = loaded
		} else {
			tz = defaultTZ
		}
		out.TimeZone = tz
		if strings.TrimSpace(c.At) != "" {
			atInstant, err := resolveTimerAtInstant(strings.TrimSpace(c.At), loc)
			if err != nil {
				return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix + ".at", Message: "at must be YYYY-MM-DDTHH:MM or RFC 3339"}
			}
			if atInstant.Before(now.Truncate(time.Minute).Add(time.Minute)) {
				return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix + ".at", Message: "at must be at least one minute ahead"}
			}
			out.At = strings.TrimSpace(c.At)
			return out, nil
		}
		calSpec, err := normalizeStandingCalendar(prefix+".calendar", *c.Calendar)
		if err != nil {
			return awaitstore.Condition{}, err
		}
		out.Calendar = &calSpec
		return out, nil

	case "git_ref":
		root, ref := strings.TrimSpace(c.Root), strings.TrimSpace(c.Ref)
		if root == "" {
			return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix + ".root", Message: "git_ref requires root"}
		}
		if len(auth.Roots) > 0 {
			if _, ok := resolveWatchRootGrant(auth, root); !ok {
				return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix + ".root", Message: "unauthorized root label"}
			}
		}
		if !strings.HasPrefix(ref, "refs/") || len(ref) <= len("refs/") || strings.Contains(ref, "..") {
			return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix + ".ref", Message: "git_ref requires a full ref beginning refs/"}
		}
		return awaitstore.Condition{Kind: "git_ref", Root: root, Ref: ref}, nil

	case "file_change":
		if c.IncludeOwn {
			return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix + ".include_own", Message: "standing watch file_change does not accept include_own"}
		}
		root, rel := strings.TrimSpace(c.Root), strings.TrimSpace(c.Path)
		if root == "" || rel == "" {
			return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix + ".path", Message: "file_change requires root and path"}
		}
		if strings.HasPrefix(rel, "/") || rel == ".." || strings.HasPrefix(path.Clean(rel), "../") {
			return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix + ".path", Message: "file_change path must be root-relative"}
		}
		if len(auth.Roots) > 0 {
			if _, ok := resolveWatchRootGrant(auth, root); !ok {
				return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix + ".root", Message: "unauthorized root label"}
			}
		}
		glob := strings.TrimSpace(c.Glob)
		if len(glob) > 256 {
			return awaitstore.Condition{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: prefix + ".glob", Message: "glob exceeds 256 characters"}
		}
		return awaitstore.Condition{Kind: "file_change", Root: root, Path: path.Clean(rel), Glob: glob}, nil

	default:
		return awaitstore.Condition{}, &ValidationError{
			Code:    "WATCH_CONDITION_INVALID",
			Field:   prefix + ".kind",
			Message: "standing watches accept only timer, git_ref, and file_change conditions",
		}
	}
}

func normalizeStandingCalendar(field string, spec awaitstore.CalendarSpec) (awaitstore.CalendarSpec, error) {
	if len(spec.Days) < 1 || len(spec.Days) > 7 {
		return awaitstore.CalendarSpec{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: field + ".days", Message: "calendar.days must contain 1 to 7 entries"}
	}
	if len(spec.Times) < 1 || len(spec.Times) > 8 {
		return awaitstore.CalendarSpec{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: field + ".times", Message: "calendar.times must contain 1 to 8 HH:MM entries"}
	}
	validDays := map[string]bool{
		"every_day": true, "weekdays": true,
		"monday": true, "tuesday": true, "wednesday": true, "thursday": true,
		"friday": true, "saturday": true, "sunday": true,
	}
	seenDays := map[string]bool{}
	days := make([]string, 0, len(spec.Days))
	for _, d := range spec.Days {
		d = strings.ToLower(strings.TrimSpace(d))
		if !validDays[d] || seenDays[d] {
			return awaitstore.CalendarSpec{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: field + ".days", Message: "invalid or duplicate calendar day"}
		}
		seenDays[d] = true
		days = append(days, d)
	}
	if (seenDays["every_day"] || seenDays["weekdays"]) && len(days) > 1 {
		return awaitstore.CalendarSpec{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: field + ".days", Message: "every_day and weekdays cannot be combined with other days"}
	}
	seenTimes := map[string]bool{}
	times := make([]string, 0, len(spec.Times))
	for _, tm := range spec.Times {
		tm = strings.TrimSpace(tm)
		if !isValidHHMM(tm) || seenTimes[tm] {
			return awaitstore.CalendarSpec{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: field + ".times", Message: "each calendar time must be a distinct HH:MM value"}
		}
		seenTimes[tm] = true
		times = append(times, tm)
	}
	return awaitstore.CalendarSpec{Days: days, Times: times}, nil
}

func isValidHHMM(tm string) bool {
	if len(tm) != 5 || tm[2] != ':' {
		return false
	}
	hh, errH := strconv.Atoi(tm[:2])
	mm, errM := strconv.Atoi(tm[3:])
	return errH == nil && errM == nil && hh >= 0 && hh <= 23 && mm >= 0 && mm <= 59
}

func insertStandingWatchTx(
	ctx context.Context,
	tx *sql.Tx,
	ownerPersonID string,
	in preparedStandingInput,
	cursors map[string]string,
	gateBaselineJSON string,
	keptFrom *StandingKeptFrom,
	now time.Time,
) (string, error) {
	if err := checkUniqueStandingNameTx(ctx, tx, in.ProjectID, in.Name, ""); err != nil {
		return "", err
	}
	watchID := uuid.NewString()
	condBytes, _ := json.Marshal(in.Conditions)
	authBytes, _ := json.Marshal(in.Authority)
	cursorBytes, _ := json.Marshal(cursors)
	targetBytes, _ := json.Marshal(in.Target)
	var gateStr, keptSess, keptHandle string
	if in.Gate != nil {
		gb, _ := json.Marshal(in.Gate)
		gateStr = string(gb)
	}
	if keptFrom != nil {
		keptSess, keptHandle = keptFrom.SessionID, keptFrom.Handle
	}
	nowStr := now.UTC().Format(time.RFC3339Nano)

	if _, err := tx.ExecContext(ctx, `INSERT INTO watch_registrations (
		watch_id, project_id, owner_kind, session_id, worker_job_id, owner_person_id,
		handle, seq, conditions_json, authority_json, cursors_json,
		delivery, repeat, expires_at, state, revision, created_at, updated_at
	) VALUES (?, ?, 'person', NULL, NULL, ?, ?, 0, ?, ?, ?, 'run', 1, NULL, 'enabled', 1, ?, ?)`,
		watchID, in.ProjectID, ownerPersonID, watchID,
		string(condBytes), string(authBytes), string(cursorBytes), nowStr, nowStr); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO standing_watch_settings (
		watch_id, project_id, name, name_folded, prompt, gate_json, gate_baseline_json,
		min_gap_minutes, target_json, workflow_id, model_id, run_spend_limit_usd,
		allow_unpriced, daily_run_cap, time_zone, interval_anchor,
		kept_from_session_id, kept_from_handle
	) VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''))`,
		watchID, in.ProjectID, in.Name, strings.ToLower(in.Name), in.Prompt, gateStr, gateBaselineJSON,
		in.MinGapMinutes, string(targetBytes), in.WorkflowID, in.ModelID, in.RunSpendLimitUSD,
		boolToInt(in.AllowUnpriced), in.DailyRunCap, in.TimeZone, nowStr, keptSess, keptHandle); err != nil {
		return "", mapStandingSQLiteErr(err, in.Name)
	}
	return watchID, nil
}

func applyReplacesKeepTx(
	ctx context.Context,
	tx *sql.Tx,
	actorPersonID string,
	in preparedStandingInput,
	replacesID string,
	baseRevision int,
	cursors map[string]string,
	gateBaselineJSON string,
	now time.Time,
) (int, error) {
	var ownerID, curState string
	var curRev int
	err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(owner_person_id, ''), state, revision FROM watch_registrations
		 WHERE watch_id = ? AND project_id = ? AND owner_kind = 'person'`,
		replacesID, in.ProjectID).Scan(&ownerID, &curState, &curRev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, &UnknownHandleError{Handle: replacesID}
	}
	if err != nil {
		return 0, err
	}
	if ownerID != actorPersonID && curState != string(StateDisabled) {
		return 0, &NotOwnerError{WatchID: replacesID}
	}
	if baseRevision > 0 && curRev != baseRevision {
		return 0, &RevisionConflictError{WatchID: replacesID, CurrentRevision: curRev}
	}
	if err := checkUniqueStandingNameTx(ctx, tx, in.ProjectID, in.Name, replacesID); err != nil {
		return 0, err
	}

	condBytes, _ := json.Marshal(in.Conditions)
	cursorBytes, _ := json.Marshal(cursors)
	targetBytes, _ := json.Marshal(in.Target)
	var gateStr string
	if in.Gate != nil {
		gb, _ := json.Marshal(in.Gate)
		gateStr = string(gb)
	}
	nowStr := now.UTC().Format(time.RFC3339Nano)
	nextRev, nextState := curRev+1, curState
	if curState == string(StateDisabled) {
		nextState, ownerID = string(StateEnabled), actorPersonID
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_runs SET status = 'canceled', ended_at = ?
		 WHERE watch_id = ? AND status = 'pending' AND run_key LIKE 'timer:%'`,
		nowStr, replacesID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE watch_registrations
		SET owner_person_id = ?, conditions_json = ?, cursors_json = ?, state = ?,
		    state_reason = CASE WHEN ? = 'enabled' THEN NULL ELSE state_reason END,
		    revision = ?, updated_at = ?
		WHERE watch_id = ?`,
		ownerID, string(condBytes), string(cursorBytes), nextState, nextState, nextRev, nowStr, replacesID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE standing_watch_settings
		SET name = ?, name_folded = ?, prompt = ?, gate_json = NULLIF(?, ''), gate_baseline_json = NULLIF(?, ''),
		    min_gap_minutes = ?, target_json = ?, workflow_id = ?, model_id = ?,
		    run_spend_limit_usd = ?, allow_unpriced = ?, daily_run_cap = ?, time_zone = ?, interval_anchor = ?
		WHERE watch_id = ?`,
		in.Name, strings.ToLower(in.Name), in.Prompt, gateStr, gateBaselineJSON,
		in.MinGapMinutes, string(targetBytes), in.WorkflowID, in.ModelID,
		in.RunSpendLimitUSD, boolToInt(in.AllowUnpriced), in.DailyRunCap, in.TimeZone, nowStr, replacesID); err != nil {
		return 0, mapStandingSQLiteErr(err, in.Name)
	}
	return nextRev, nil
}
