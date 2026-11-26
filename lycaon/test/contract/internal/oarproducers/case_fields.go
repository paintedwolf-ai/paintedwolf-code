package oarproducers

import (
	"reflect"
	"strconv"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
)

// factField locates the GuardContext field an occurrence producer sets for a
// fact: the production projection is probed field by field until setting a
// field alone makes the fact read the probe value.
type factField struct {
	index []int
	kind  reflect.Kind
}

const fieldProbe = "casegen-probe"

// discoverFactField finds the typed field behind fact, or false when the
// production projection reads it from somewhere else.
func discoverFactField(fact string, typ oarcore.FactType) (factField, bool) {
	var probe, cond string
	switch typ {
	case oarcore.TypeBool:
		cond = fact
	case oarcore.TypeString:
		cond, probe = fact+` == "`+fieldProbe+`"`, fieldProbe
	case oarcore.TypeInt:
		cond = fact + " == 7"
	case oarcore.TypeListString:
		cond = strconv.Quote(fieldProbe) + " in " + fact
	default:
		return factField{}, false
	}
	var found factField
	ok := false
	walkGuardFields(reflect.TypeOf(oar.GuardContext{}), nil, func(index []int, f reflect.StructField) bool {
		gc := oar.NewGuardContext()
		v := reflect.ValueOf(gc).Elem().FieldByIndex(index)
		switch {
		case typ == oarcore.TypeBool && f.Type.Kind() == reflect.Bool:
			v.SetBool(true)
		case typ == oarcore.TypeString && f.Type.Kind() == reflect.String:
			v.SetString(probe)
		case typ == oarcore.TypeInt && isInt(f.Type.Kind()):
			v.SetInt(7)
		case typ == oarcore.TypeListString && f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.String:
			v.Set(reflect.ValueOf([]string{fieldProbe}).Convert(f.Type))
		default:
			return false
		}
		if held, err := oar.EvaluateCondition(cond, gc); err == nil && held {
			found, ok = factField{index: index, kind: f.Type.Kind()}, true
			return true
		}
		return false
	})
	return found, ok
}

func isInt(k reflect.Kind) bool {
	return k == reflect.Int || k == reflect.Int64 || k == reflect.Int32
}

// walkGuardFields visits the exported scalar and string-list fields of the
// fact groups, stopping when visit reports a match.
func walkGuardFields(t reflect.Type, prefix []int, visit func([]int, reflect.StructField) bool) bool {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		index := append(append([]int(nil), prefix...), i)
		switch f.Type.Kind() {
		case reflect.Struct:
			if walkGuardFields(f.Type, index, visit) {
				return true
			}
		case reflect.Bool, reflect.String, reflect.Int, reflect.Int32, reflect.Int64, reflect.Slice:
			if len(prefix) > 0 && visit(index, f) {
				return true
			}
		}
	}
	return false
}

// set stores value in gc's field.
func (f factField) set(gc *oar.GuardContext, value any) {
	v := reflect.ValueOf(gc).Elem().FieldByIndex(f.index)
	switch x := value.(type) {
	case bool:
		v.SetBool(x)
	case string:
		v.SetString(x)
	case int64:
		v.SetInt(x)
	case []string:
		v.Set(reflect.ValueOf(x).Convert(v.Type()))
	}
}
