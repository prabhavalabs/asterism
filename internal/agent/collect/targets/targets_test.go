package targets

import "testing"

func TestRegistry(t *testing.T) {
	r := New()
	g0 := r.Gen()
	id := "317e4d06ef07a1453738080b7345f935f359dd2c0521ffffcf9bb62ab05fed25"
	c := Container{EntityID: "ctr:a", Name: "a", FullID: id, Pid: 1, Running: true, HostPorts: map[uint32]uint32{8080: 80}}
	r.SetContainer(c)
	g1 := r.Gen()
	if g1 == g0 {
		t.Fatal("gen unchanged")
	}
	r.SetContainer(c)
	if r.Gen() != g1 {
		t.Fatal("identical set bumped gen")
	}
	if got, ok := r.ContainerByID(id[:12]); !ok || got.Name != "a" {
		t.Fatal("prefix lookup")
	}
	if _, ok := r.ContainerByID("317e"); ok {
		t.Fatal("short prefix accepted")
	}
	if got, cp, ok := r.ContainerByHostPort(8080); !ok || cp != 80 || got.Name != "a" {
		t.Fatal("host port")
	}
	r.SetUnits([]Unit{{Name: "nginx.service", Dir: "/x"}})
	g2 := r.Gen()
	r.SetUnits([]Unit{{Name: "nginx.service", Dir: "/x"}})
	if r.Gen() != g2 || len(r.Units(nil)) != 1 {
		t.Fatal("units")
	}
	r.SetProcs("proc:a", []int{10, 11})
	r.SetProcs("proc:a", []int{11})
	if r.ProcOwner(10) != "" || r.ProcOwner(11) != "proc:a" {
		t.Fatal("procs")
	}
	r.SetListeningPids([]int{5})
	if !r.IsListening(5) || r.IsListening(6) {
		t.Fatal("listening")
	}
	r.RemoveContainer(id)
	if len(r.Containers(nil)) != 0 || r.Gen() == g2 {
		t.Fatal("remove")
	}
}
