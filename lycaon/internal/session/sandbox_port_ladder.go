package session

import "github.com/lycaon/lycaon/internal/hitl"

// portAuthorityLadder builds port authority options for one chat.
func portAuthorityLadder(
	onceID, onceCoverage, onceReask string,
	chatKind hitl.AuthorityDeltaKind,
	chatGrant hitl.ApprovalGrant,
	chatSessionID string,
	listen bool,
) []hitl.ApprovalOption {
	once := hitl.CurrentActionOption()
	once.ID = onceID
	once.Coverage = onceCoverage
	once.ReaskWhen = onceReask

	chatDelta := hitl.ApprovalAuthorityDelta{
		Kind: chatKind, Grant: &chatGrant, ChatSessionID: chatSessionID,
	}
	if listen {
		chatDelta.ListenPorts = nil
	} else {
		chatDelta.ConnectPorts = nil
	}
	chatOffer := hitl.ApprovalGrantOffer{
		ID: chatGrant.ID, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat,
		Title: chatGrant.Title, Coverage: chatGrant.Coverage,
		ExpiresWhen: chatGrant.ExpiresWhen, ReaskWhen: chatGrant.ReaskWhen,
		Grant: chatGrant, Authority: []hitl.ApprovalAuthorityDelta{chatDelta},
	}
	day := hitl.DayRung(chatOffer)
	dayDelta := chatDelta
	dayDelta.Grant = &day.Grant
	dayDelta.TTLSeconds = day.TTLSeconds
	day.Authority = []hitl.ApprovalAuthorityDelta{dayDelta}

	// Local-network authority lasts only for the chat, so the durable slot stays in
	// place and says so rather than moving the rungs below it.
	project := chatOffer
	project.ID = chatGrant.ID + "-project"
	project.Grant.ID = project.ID
	project.Rung = hitl.ApprovalRungProject
	project.Scope = hitl.ApprovalGrantScopeProject
	project.Grant.Scope = hitl.ApprovalGrantScopeProject
	project.Title = hitl.TitleAllowForThisProject
	project.Grant.Title = project.Title
	project.ExpiresWhen = hitl.ExpiresIn7DaysOrRevoked
	project.Grant.ExpiresWhen = project.ExpiresWhen
	projectDelta := chatDelta
	projectDelta.Grant = &project.Grant
	project.Authority = []hitl.ApprovalAuthorityDelta{projectDelta}

	return []hitl.ApprovalOption{
		once, hitl.GrantOption(day), hitl.GrantOption(chatOffer),
		hitl.GrantOption(hitl.DisabledOffer(project, hitl.NoteEndsWithChat)),
	}
}
