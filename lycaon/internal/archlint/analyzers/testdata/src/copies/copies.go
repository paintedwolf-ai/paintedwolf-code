package copies

type Snapshot struct {
	ID   string
	Seq  int
	Asks int
	note string
}

type View struct {
	ID   string
	Seq  int
	Asks int
	note string
}

func project(s Snapshot) View {
	return View{ID: s.ID, Seq: s.Seq} // want `View built from s \(Snapshot\) omits shared field Asks`
}

func complete(s *Snapshot) View {
	return View{ID: s.ID, Seq: s.Seq, Asks: s.Asks}
}

func single(s Snapshot) View { return View{ID: s.ID} }
