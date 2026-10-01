package promptattach

// TurnPreviewBudget allocates preview bytes across one prompt turn.
type TurnPreviewBudget struct {
	remaining          int
	maxBodyPreview     int
	maxLargeText       int
	largeTextThreshold int64
}

// NewTurnPreviewBudget starts a request-scoped preview budget.
func NewTurnPreviewBudget(caps Caps) *TurnPreviewBudget {
	return &TurnPreviewBudget{
		remaining:          caps.Prompt.MaxTurnPreview.Int(),
		maxBodyPreview:     caps.Prompt.MaxBodyPreview.Int(),
		maxLargeText:       caps.Prompt.MaxLargeTextPreview.Int(),
		largeTextThreshold: int64(caps.Composer.AutoAttachPaste),
	}
}

func (b *TurnPreviewBudget) claim(total int64, largeText bool) int {
	if total <= 0 || b.remaining <= 0 {
		return 0
	}
	limit := b.maxBodyPreview
	if largeText && total >= b.largeTextThreshold {
		limit = b.maxLargeText
	}
	if limit > b.remaining {
		limit = b.remaining
	}
	if int64(limit) > total {
		limit = int(total)
	}
	b.remaining -= limit
	return limit
}

func (b *TurnPreviewBudget) claimBody(total int64) int {
	return b.claim(total, false)
}

func (b *TurnPreviewBudget) claimText(total int64) int {
	return b.claim(total, true)
}
