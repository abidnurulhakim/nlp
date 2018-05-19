// Package nlp provides general purpose Natural Language Processing.
package nlp

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"
	"unicode"

	"encoding/json"

	"github.com/cdipaolo/goml/base"
	"github.com/cdipaolo/goml/text"
	"github.com/itrabbit/nlp/parser"
)

// NL is a Natural Language Processor
type NL struct {
	models []*model
	naive  *text.NaiveBayes
	// Output contains the training output for the
	// NaiveBayes algorithm
	Output *bytes.Buffer
}

// Structures for Export/Import
type wordSaved struct {
	Count    []uint64 `json:"c"`
	Seen     uint64   `json:"s"`
	DocsSeen uint64   `json:"ds"`
}
type naiveBayesSaved struct {
	Words         map[string]wordSaved   `json:"w"`
	Count         []uint64               `json:"c"`
	Probabilities []float64              `json:"p"`
	DocumentCount uint64                 `json:"d"`
	DictCount     uint64                 `json:"v"`
	Tokenizer     map[string]interface{} `json:"t,omitempty"`
}
type itemSaved struct {
	Limit      bool   `json:"l,omitempty"`
	Value      []byte `json:"v,omitempty"`
	FieldIndex int    `json:"f,omitempty"`
}
type modelSaved struct {
	Type     string        `json:"t"`
	Expected [][]itemSaved `json:"e"`
	Samples  [][]byte      `json:"s"`
}

// For save load alg
type nlSaved struct {
	Models     []modelSaved    `json:"m,omitempty"`
	NaiveBayes naiveBayesSaved `json:"n,omitempty"`
	Output     []byte          `json:"o,omitempty"`
}

func (n nlSaved) indexOfModelByType(tpy reflect.Type) int {
	t := tpy.Name()
	for i, model := range n.Models {
		if model.Type == t {
			return i
		}
	}
	return -1
}

// New returns a *NL
func New() *NL { return &NL{Output: bytes.NewBufferString("")} }

// Export
func (nl NL) Export() ([]byte, error) {
	naive := naiveBayesSaved{
		Words:         make(map[string]wordSaved),
		Count:         nl.naive.Count,
		Probabilities: nl.naive.Probabilities,
		DocumentCount: nl.naive.DocumentCount,
		DictCount:     nl.naive.DictCount,
	}
	v := reflect.Indirect(reflect.ValueOf(nl.naive.Words))
	words := reflect.Indirect(v.FieldByName("words"))
	if words.Kind() == reflect.Map {
		for _, key := range words.MapKeys() {
			value := words.MapIndex(key)
			if !value.IsValid() {
				continue
			}
			if value.Kind() != reflect.Struct {
				continue
			}
			s := value.FieldByName("Seen").Uint()
			ds := value.FieldByName("DocsSeen").Uint()
			c := make([]uint64, 0)
			countVal := reflect.Indirect(value.FieldByName("Count"))
			if countVal.Kind() == reflect.Slice || countVal.Kind() == reflect.Array {
				for i := 0; i < countVal.Len(); i++ {
					c = append(c, countVal.Index(i).Uint())
				}
			}
			naive.Words[key.String()] = wordSaved{
				Count:    c,
				Seen:     s,
				DocsSeen: ds,
			}
		}
	}
	models := make([]modelSaved, len(nl.models), len(nl.models))
	for i, model := range nl.models {
		e := make([][]itemSaved, len(model.expected), len(model.expected))
		for j, arr := range model.expected {
			sub := make([]itemSaved, len(arr), len(arr))
			for y, item := range arr {
				sub[y] = itemSaved{
					Limit:      item.limit,
					Value:      item.value,
					FieldIndex: item.field.index,
				}
			}
			e[j] = sub
		}
		models[i].Expected = e
		models[i].Samples = model.samples
		models[i].Type = model.tpy.Name()
	}
	m := map[string]interface{}{
		"n": &naive,
		"m": models,
		"o": nl.Output.Bytes(),
	}
	return json.Marshal(&m)
}

// Import
func (nl *NL) Import(p []byte) error {
	s := nlSaved{}
	if err := json.Unmarshal(p, &s); err != nil {
		return err
	}
	nl.naive = text.NewNaiveBayes(nil, uint8(len(nl.models)), base.OnlyWordsAndNumbers)
	for k, v := range s.NaiveBayes.Words {
		nl.naive.Words.Set(k, text.Word{
			Count:    v.Count,
			Seen:     v.Seen,
			DocsSeen: v.DocsSeen,
		})
	}
	nl.naive.DictCount = s.NaiveBayes.DictCount
	nl.naive.DocumentCount = s.NaiveBayes.DocumentCount
	nl.naive.Probabilities = s.NaiveBayes.Probabilities
	nl.naive.Count = s.NaiveBayes.Count
	nl.Output = &bytes.Buffer{}
	nl.Output.Write(s.Output)
	nl.naive.Output = nl.Output
	if len(nl.models) != len(s.Models) {
		return fmt.Errorf("invalid models")
	}
	for _, model := range nl.models {
		index := s.indexOfModelByType(model.tpy)
		if index < 0 {
			continue
		}
		savedModel := s.Models[index]
		model.expected = make([][]item, len(savedModel.Expected), len(savedModel.Expected))
		for i, arr := range savedModel.Expected {
			a := make([]item, len(arr), len(arr))
			for j, obj := range arr {
				a[j] = item{
					limit: obj.Limit,
					value: obj.Value,
					field: model.fields[obj.FieldIndex],
				}
			}
			model.expected[i] = a
		}
		model.samples = savedModel.Samples
	}
	return nil
}

// P proccesses the expr and returns one of
// the types passed as the i parameter to the RegistryModel
// func filled with the data inside expr
func (nl *NL) P(expr string) interface{} { return nl.models[nl.naive.Predict(expr)].fit(expr) }

// Learn maps the models samples to the models themselves and
// returns an error if something occurred while learning
func (nl *NL) Learn() error {
	if len(nl.models) > 0 {
		stream := make(chan base.TextDatapoint)
		errors := make(chan error)
		nl.naive = text.NewNaiveBayes(stream, uint8(len(nl.models)), base.OnlyWordsAndNumbers)
		nl.naive.Output = nl.Output
		go nl.naive.OnlineLearn(errors)
		for i := range nl.models {
			err := nl.models[i].learn()
			if err != nil {
				return fmt.Errorf("model#%d %v", i, err)
			}
			for _, s := range nl.models[i].samples {
				stream <- base.TextDatapoint{
					X: string(s),
					Y: uint8(i),
				}
			}
		}
		close(stream)
		for {
			err := <-errors
			if err != nil {
				return fmt.Errorf("error occurred while learning: %s", err)
			}
			// training is done!
			break
		}
		return nil
	}
	return fmt.Errorf("register at least one model before learning")
}

type model struct {
	tpy          reflect.Type
	fields       []field
	expected     [][]item
	samples      [][]byte
	timeFormat   string
	timeLocation *time.Location
}

type item struct {
	limit bool
	value []byte
	field field
}

type field struct {
	index int
	name  string
	kind  interface{}
}

// ModelOption is an option for a specific model
type ModelOption func(*model) error

// WithTimeFormat sets the format used in time.Parse(format, val),
// note that format can't contain any spaces, the default is 01-02-2006_3:04pm
func WithTimeFormat(format string) ModelOption {
	return func(m *model) error {
		for _, v := range format {
			if unicode.IsSpace(v) {
				return errors.New("time format can't contain any spaces")
			}
		}
		m.timeFormat = format
		return nil
	}
}

// WithTimeLocation sets the location used in time.ParseInLocation(format, value, loc),
// the default is time.Local
func WithTimeLocation(loc *time.Location) ModelOption {
	return func(m *model) error {
		if loc == nil {
			return errors.New("time location can't be nil")
		}
		m.timeLocation = loc
		return nil
	}
}

// RegisterModel registers a model i and creates possible patterns
// from samples, the default layout when parsing time is 01-02-2006_3:04pm
// and the default location is time.Local.
// Samples must have special formatting:
//
//	"play {Name} by {Artist}"
func (nl *NL) RegisterModel(i interface{}, samples []string, ops ...ModelOption) error {
	if i == nil {
		return fmt.Errorf("can't create model from nil value")
	}
	//if len(samples) == 0 {
	// return fmt.Errorf("samples can't be nil or empty")
	//}
	tpy, val := reflect.TypeOf(i), reflect.ValueOf(i)
	if tpy.Kind() == reflect.Struct {
		mod := &model{
			tpy:          tpy,
			expected:     make([][]item, len(samples)),
			timeFormat:   "01-02-2006_3:04pm",
			timeLocation: time.Local,
		}
		mod.setSamples(samples)
		for _, op := range ops {
			err := op(mod)
			if err != nil {
				return err
			}
		}
	NextField:
		for i := 0; i < tpy.NumField(); i++ {
			if tpy.Field(i).Anonymous || tpy.Field(i).PkgPath != "" {
				continue NextField
			}
			if v, ok := val.Field(i).Interface().(time.Time); ok {
				mod.fields = append(mod.fields, field{i, tpy.Field(i).Name, v})
				continue NextField
			} else if v, ok := val.Field(i).Interface().(time.Duration); ok {
				mod.fields = append(mod.fields, field{i, tpy.Field(i).Name, v})
				continue NextField
			}
			switch val.Field(i).Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Float32, reflect.Float64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.String:
				mod.fields = append(mod.fields, field{i, tpy.Field(i).Name, val.Field(i).Kind()})
			}
		}
		nl.models = append(nl.models, mod)
		return nil
	}
	return fmt.Errorf("can't create model from non-struct type")
}

func (m *model) learn() error {
	for sid, s := range m.samples {
		tokens, err := parser.ParseSample(sid, s)
		if err != nil {
			return err
		}
		var exps []item
		var hasAtLeastOneKey bool
		l := len(tokens)
		for i, tk := range tokens {
			if tk.Kw {
				hasAtLeastOneKey = true
				mistypedField := true
				for _, f := range m.fields {
					if string(tk.Val) == f.name {
						mistypedField = false
						exps = append(exps, item{field: f, value: tk.Val})
					}
				}
				if mistypedField {
					return fmt.Errorf("sample#%d: mistyped field %q", sid, tk.Val)
				}
			} else {
				if i+1 < l {
					if tokens[i+1].Kw {
						exps = append(exps, item{limit: true, value: tk.Val})
						continue
					}
				}
			}
		}
		if !hasAtLeastOneKey {
			return fmt.Errorf("sample#%d: need at least one keyword", sid)
		}
		m.expected[sid] = exps
	}
	return nil
}

func (m *model) selectBestSample(expr []byte) []item {
	// slice [sample_id]score
	scores := make([]int, len(m.samples))

	tokens, _ := parser.ParseSample(0, expr)

	mapping := make([][]item, len(m.samples))
	limitsOrder := make([][][]byte, len(m.samples)+1)

	for sid, exps := range m.expected {
		var currentVal [][]byte
		var reading bool
		var lastToken int
	expecteds:
		for _, e := range exps {
			// fmt.Printf("expecting: %s - limit: %v\n", e.value, e.limit)
			if e.limit {
				reading = false
				limitsOrder[sid+1] = append(limitsOrder[sid+1], e.value)
			} else {
				reading = true
			}
			// fmt.Printf("reading: %v\n", reading)
			for i := lastToken; i < len(tokens); i++ {
				t := tokens[i]
				// fmt.Printf("token: %s - isLimit: %v\n", t.Val, m.isLimit(t.Val, sid))
				if m.isLimit(t.Val, sid) {
					if sid == 0 {
						limitsOrder[0] = append(limitsOrder[0], t.Val)
					}
					scores[sid]++
					if len(currentVal) > 0 {
						// fmt.Printf("appending: %s {%v}\n", bytes.Join(currentVal, []byte{' '}), e.field.name)
						mapping[sid] = append(mapping[sid], item{field: e.field, value: bytes.Join(currentVal, []byte{' '})})
						currentVal = currentVal[:0]
						lastToken = i
						continue expecteds
					}
					lastToken = i + 1
					continue expecteds
				} else {
					if reading {
						// fmt.Printf("adding: %s\n", t.Val)
						currentVal = append(currentVal, t.Val)
					}
				}
			}
			if len(currentVal) > 0 {
				// fmt.Printf("appending: %s {%v}\n", bytes.Join(currentVal, []byte{' '}), e.field.name)
				mapping[sid] = append(mapping[sid], item{field: e.field, value: bytes.Join(currentVal, []byte{' '})})
			}
		}
		// fmt.Printf("\n\n")
	}
order:
	for i := 1; i < len(limitsOrder); i++ {
		if len(limitsOrder[0]) < len(limitsOrder[i]) {
			continue order
		}
		for j := range limitsOrder[i] {
			if !bytes.Equal(limitsOrder[i][j], limitsOrder[0][j]) {
				continue order
			}
		}
		scores[i-1]++
	}

	// fmt.Printf("orders: %s\n\n", limitsOrder)
	// fmt.Printf("scores: %v\n", scores)

	bestMapping := selectBestMapping(scores)
	if bestMapping == -1 {
		return nil
	}
	return mapping[bestMapping]
}

func selectBestMapping(scores []int) int {
	bestScore, bestMapping := -1, -1
	for id, score := range scores {
		if score > bestScore {
			bestScore = score
			bestMapping = id
		}
	}
	return bestMapping
}

func (m *model) fit(expr string) interface{} {
	val := reflect.New(m.tpy)
	if len(expr) == 0 {
		return val.Interface()
	}
	exps := m.selectBestSample([]byte(expr))
	if len(exps) > 0 {
		for _, e := range exps {
			switch t := e.field.kind.(type) {
			case reflect.Kind:
				switch t {
				case reflect.String:
					val.Elem().Field(e.field.index).SetString(string(e.value))
				case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					v, _ := strconv.ParseUint(string(e.value), 10, 0)
					val.Elem().Field(e.field.index).SetUint(v)
				case reflect.Float32, reflect.Float64:
					v, _ := strconv.ParseFloat(string(e.value), 64)
					val.Elem().Field(e.field.index).SetFloat(v)
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					v, _ := strconv.ParseInt(string(e.value), 10, 0)
					val.Elem().Field(e.field.index).SetInt(v)
				}
			case time.Time:
				v, _ := time.ParseInLocation(m.timeFormat, string(e.value), m.timeLocation)
				val.Elem().Field(e.field.index).Set(reflect.ValueOf(v))
			case time.Duration:
				v, _ := time.ParseDuration(string(e.value))
				val.Elem().Field(e.field.index).Set(reflect.ValueOf(v))
			}
		}
	}
	return val.Interface()
}

// isLimit returns true if s is a limit on expected[id]
func (m *model) isLimit(s []byte, id int) bool {
	for _, e := range m.expected[id] {
		if bytes.Equal(e.value, s) {
			return true
		}
	}
	return false
}

// setSample converts the []string samples to [][]byte
func (m *model) setSamples(samples []string) {
	for _, s := range samples {
		m.samples = append(m.samples, []byte(s))
	}
}
