package mutation

import "github.com/lycaon/lycaon/internal/tools"

func (inv *invocation) applySyntaxOverride() error {
	reason, present := inv.value("--syntax-override-reason")
	if !present || inv.flag("--dry-run") {
		return nil
	}
	ctx, err := tools.WithSyntaxOverride(inv.ctx, map[string]any{"syntax_override_reason": reason})
	if err != nil {
		return err
	}
	inv.ctx = ctx
	return nil
}
