package textsearch

const contextLineBytes = 128

type retainedLine struct {
	line    Line
	release func()
}

type contextLines struct {
	memory  scanMemory
	maximum int
	lines   []retainedLine
	oldest  int
	count   int
	release func()
}

func (p *contextLines) grow() error {
	if p.count < len(p.lines) {
		return nil
	}
	capacity := min(p.maximum, max(8, 2*len(p.lines)))
	release, err := p.memory.acquire(int64(capacity) * contextLineBytes)
	if err != nil {
		return err
	}
	next := make([]retainedLine, capacity)
	for i := range p.count {
		next[i] = p.lines[(p.oldest+i)%len(p.lines)]
	}
	oldRelease := p.release
	p.lines, p.oldest, p.release = next, 0, release
	if oldRelease != nil {
		oldRelease()
	}
	return nil
}

func (p *contextLines) pop() (Line, func()) {
	next := p.lines[p.oldest]
	p.lines[p.oldest] = retainedLine{}
	p.oldest = (p.oldest + 1) % len(p.lines)
	p.count--
	return next.line, next.release
}

func (p *contextLines) push(line Line) error {
	if p.maximum <= 0 {
		return nil
	}
	if p.count == p.maximum {
		_, release := p.pop()
		release()
	}
	if err := p.grow(); err != nil {
		return err
	}
	release, err := p.memory.acquire(int64(len(line.Text)))
	if err != nil {
		return err
	}
	text := make([]byte, len(line.Text))
	copy(text, line.Text)
	line.Text = text
	at := (p.oldest + p.count) % len(p.lines)
	p.lines[at] = retainedLine{line: line, release: release}
	p.count++
	return nil
}

func (p *contextLines) close() {
	for p.count > 0 {
		_, release := p.pop()
		release()
	}
	p.lines = nil
	if p.release != nil {
		p.release()
	}
}
