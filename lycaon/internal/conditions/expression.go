package conditions

import (
 "fmt"

 "github.com/lycaon/lycaon/internal/boolexpr"
 "github.com/lycaon/lycaon/internal/vocabulary"
)

// ParseExpression resolves every name before boolean evaluation can skip a branch.
func (r *ConditionRegistry) ParseExpression(expr string)(boolexpr.Node,error) {
 node,err:=boolexpr.Parse(expr)
 if err!=nil{return nil,err}
 for _,name:=range boolexpr.CollectIdents(node) {
 if err:=r.Resolve(name);err!=nil{return nil,err}
 }
 return node,nil
}

func (r *ConditionRegistry) Resolve(name string) error {
 if vocabulary.IsForbidden(name) || vocabulary.IsHostOnlyCondition(name) {return fmt.Errorf("%w: %s",ErrForbiddenCondition,name)}
 if !r.Has(name){return fmt.Errorf("%w: %s",ErrUnknownCondition,name)}
 return nil
}
