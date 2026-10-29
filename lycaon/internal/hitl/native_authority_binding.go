package hitl

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

// authorityBinding converts the known host authority structs to/from native
// scalar/container values. It never serializes, screens or installs authority.
// Every field is present, including zero values, so unknown/missing shapes fail.
type authorityBinding struct {
	ctx      context.Context
	state    toolcontract.ReviewStateAdmission
	releases []func()
}

func (b *authorityBinding) reserve(n int64) error {
	if n < 0 || n > (math.MaxInt64-2048)/16 {
		return fmt.Errorf("native authority binding bound overflow")
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	release, err := b.state.ReserveProgramState(b.ctx, 2048+16*n)
	if err != nil {
		return err
	}
	if release == nil {
		return fmt.Errorf("native authority binding requires retained admission")
	}
	b.releases = append(b.releases, release)
	return nil
}
func (b *authorityBinding) close() {
	for n := len(b.releases) - 1; n >= 0; n-- {
		b.releases[n]()
	}
	b.releases = nil
}
func (b *authorityBinding) release() func() { var once sync.Once; return func() { once.Do(b.close) } }

var authorityTimeType = reflect.TypeFor[time.Time]()

func authorityKnownType(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		return authorityKnownType(t.Elem())
	}
	switch t {
	case reflect.TypeFor[string](), reflect.TypeFor[bool](), reflect.TypeFor[int](), reflect.TypeFor[uint16](), authorityTimeType,
		reflect.TypeFor[ApprovalOption](), reflect.TypeFor[ApprovalOptionKind](), reflect.TypeFor[ApprovalOptionRung](), reflect.TypeFor[ApprovalOptionDecision](),
		reflect.TypeFor[ApprovalGrantScope](), reflect.TypeFor[ApprovalAuthorityDelta](), reflect.TypeFor[AuthorityDeltaKind](),
		reflect.TypeFor[ApprovalGrant](), reflect.TypeFor[ApprovalGrantPredicate](), reflect.TypeFor[ApprovalGrantWitness](),
		reflect.TypeFor[ApprovalSocketTarget](), reflect.TypeFor[DirectIPLease](), reflect.TypeFor[GrantedPathDelta](), reflect.TypeFor[AskQuietDelta](), reflect.TypeFor[TrustDestinationDelta](),
		reflect.TypeFor[authzledger.PolicyIdentity](), reflect.TypeFor[secretmatch.Recipient](), reflect.TypeFor[secretmatch.ScreenSurface](), reflect.TypeFor[secretmatch.DestinationKind](), reflect.TypeFor[api.ElevatedAccessEffect]():
		return true
	default:
		return false
	}
}

func authorityFieldName(field reflect.StructField) (string, error) {
	if field.Anonymous {
		return "", fmt.Errorf("native authority has an unsupported embedded field")
	}
	if field.PkgPath != "" {
		return "", nil
	}
	tag := field.Tag.Get("json")
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return "", nil
	}
	if name == "" {
		name = field.Name
	}
	return name, nil
}

func (b *authorityBinding) value(v reflect.Value) (any, error) {
	if !authorityKnownType(v.Type()) {
		return nil, fmt.Errorf("native authority host type is not declared")
	}
	if err := b.reserve(int64(v.Type().Size())); err != nil {
		return nil, err
	}
	if v.Type() == authorityTimeType {
		return v.Interface().(time.Time).Format(time.RFC3339Nano), nil
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil, nil
		}
		return b.value(v.Elem())
	case reflect.String:
		return v.String(), nil
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return json.Number(strconv.FormatInt(v.Int(), 10)), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return json.Number(strconv.FormatUint(v.Uint(), 10)), nil
	case reflect.Slice:
		if v.IsNil() {
			return nil, nil
		}
		if err := b.reserve(int64(v.Len()) * 32); err != nil {
			return nil, err
		}
		items := make([]any, v.Len())
		for n := range v.Len() {
			item, err := b.value(v.Index(n))
			if err != nil {
				return nil, err
			}
			items[n] = item
		}
		return items, nil
	case reflect.Struct:
		if err := b.reserve(int64(v.NumField()) * 128); err != nil {
			return nil, err
		}
		fields := make(map[string]any, v.NumField())
		for n := range v.NumField() {
			name, err := authorityFieldName(v.Type().Field(n))
			if err != nil {
				return nil, err
			}
			if name == "" {
				continue
			}
			if _, exists := fields[name]; exists {
				return nil, fmt.Errorf("native authority has ambiguous fields")
			}
			item, err := b.value(v.Field(n))
			if err != nil {
				return nil, err
			}
			fields[name] = item
		}
		return fields, nil
	default:
		return nil, fmt.Errorf("native authority has unsupported host field %s", v.Type())
	}
}

func (b *authorityBinding) assign(dst reflect.Value, raw any) error {
	if !authorityKnownType(dst.Type()) {
		return fmt.Errorf("native authority target type is not declared")
	}
	if err := b.reserve(int64(dst.Type().Size())); err != nil {
		return err
	}
	if dst.Type() == authorityTimeType {
		text, ok := raw.(string)
		if !ok {
			return fmt.Errorf("native authority timestamp shape invalid")
		}
		value, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return err
		}
		if value.Format(time.RFC3339Nano) != text {
			return fmt.Errorf("native authority timestamp is not canonical")
		}
		dst.Set(reflect.ValueOf(value))
		return nil
	}
	if raw == nil {
		if dst.Kind() != reflect.Pointer && dst.Kind() != reflect.Slice {
			return fmt.Errorf("native authority null scalar")
		}
		dst.SetZero()
		return nil
	}
	switch dst.Kind() {
	case reflect.Pointer:
		if err := b.reserve(int64(dst.Type().Elem().Size())); err != nil {
			return err
		}
		value := reflect.New(dst.Type().Elem())
		if err := b.assign(value.Elem(), raw); err != nil {
			return err
		}
		dst.Set(value)
		return nil
	case reflect.String:
		value, ok := raw.(string)
		if !ok {
			return fmt.Errorf("native authority string shape invalid")
		}
		dst.SetString(value)
		return nil
	case reflect.Bool:
		value, ok := raw.(bool)
		if !ok {
			return fmt.Errorf("native authority boolean shape invalid")
		}
		dst.SetBool(value)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		number, ok := raw.(json.Number)
		if !ok {
			return fmt.Errorf("native authority integer shape invalid")
		}
		value, err := strconv.ParseInt(string(number), 10, dst.Type().Bits())
		if err != nil {
			return err
		}
		dst.SetInt(value)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		number, ok := raw.(json.Number)
		if !ok {
			return fmt.Errorf("native authority unsigned shape invalid")
		}
		value, err := strconv.ParseUint(string(number), 10, dst.Type().Bits())
		if err != nil {
			return err
		}
		dst.SetUint(value)
		return nil
	case reflect.Slice:
		items, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("native authority list shape invalid")
		}
		size := int64(dst.Type().Elem().Size())
		if size > 0 && int64(len(items)) > math.MaxInt64/size {
			return fmt.Errorf("native authority list overflow")
		}
		if err := b.reserve(int64(len(items)) * size); err != nil {
			return err
		}
		value := reflect.MakeSlice(dst.Type(), len(items), len(items))
		for n, item := range items {
			if err := b.assign(value.Index(n), item); err != nil {
				return err
			}
		}
		dst.Set(value)
		return nil
	case reflect.Struct:
		fields, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("native authority object shape invalid")
		}
		known := 0
		for n := range dst.NumField() {
			name, err := authorityFieldName(dst.Type().Field(n))
			if err != nil {
				return err
			}
			if name == "" {
				continue
			}
			item, exists := fields[name]
			if !exists {
				return fmt.Errorf("native authority field %s absent", name)
			}
			if err := b.assign(dst.Field(n), item); err != nil {
				return err
			}
			known++
		}
		if known != len(fields) {
			return fmt.Errorf("native authority has unknown fields")
		}
		return nil
	default:
		return fmt.Errorf("native authority binding target unsupported: %s", dst.Type())
	}
}
