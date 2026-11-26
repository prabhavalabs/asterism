package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/prabhavalabs/asterism/internal/apiclient"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func newTopoCmd() *cobra.Command {
	var v view
	var around, category string
	var depth int
	cmd := &cobra.Command{
		Use:   "topo",
		Short: "Draw the topology as an ASCII tree, from entry points to data stores",
		Long: `Draw the topology as an ASCII tree: every entry point (usually a proxy or
tunnel) with the workloads it routes to, and what those talk to.

--around narrows the graph to one workload (ctr:<name>, unit:<name>) or one
project (proj:<name>) and its neighbours within --depth hops.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if depth < 1 || depth > 3 {
				return fmt.Errorf("--depth must be 1, 2 or 3")
			}
			return v.run(cmd, func(ctx context.Context, c *apiclient.Client) error {
				resp, err := call(ctx, c.GetGraph, &asterismv1.GetGraphRequest{Category: category})
				if err != nil {
					return err
				}
				g, err := filterGraph(resp.GetGraph(), around, depth)
				if err != nil {
					return err
				}
				if v.json {
					return writeProto(v.w, &asterismv1.GetGraphResponse{Graph: g})
				}
				renderTopo(v.w, v.th, g)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&around, "around", "", "Centre on a workload (ctr:x, unit:x) or project (proj:x)")
	cmd.Flags().IntVar(&depth, "depth", 1, "Hops to include around --around (1-3)")
	cmd.Flags().StringVar(&category, "category", "", "Only projects of this category")
	v.bind(cmd)
	return cmd
}

// filterGraph keeps the nodes within depth hops of around (a node id, a
// project id, or a bare node label / project name). Empty around keeps all.
func filterGraph(g *asterismv1.Graph, around string, depth int) (*asterismv1.Graph, error) {
	around = strings.TrimSpace(around)
	if around == "" || g == nil {
		return g, nil
	}
	keep := map[string]bool{}
	for _, n := range g.GetNodes() {
		if n.GetId() == around {
			keep[n.GetId()] = true
		}
	}
	if len(keep) == 0 {
		pid := projectID(around)
		for _, n := range g.GetNodes() {
			if n.GetProjectId() == pid {
				keep[n.GetId()] = true
			}
		}
	}
	if len(keep) == 0 && !strings.Contains(around, ":") {
		for _, n := range g.GetNodes() {
			if n.GetLabel() == around {
				keep[n.GetId()] = true
			}
		}
	}
	if len(keep) == 0 {
		return nil, fmt.Errorf("nothing in the topology matches %q; use a workload id (ctr:<name>, unit:<name>) or proj:<name> from `asterism ps`", around)
	}
	for range depth {
		next := map[string]bool{}
		for _, e := range g.GetEdges() {
			if keep[e.GetSourceId()] || keep[e.GetTargetId()] {
				next[e.GetSourceId()], next[e.GetTargetId()] = true, true
			}
		}
		for id := range next {
			keep[id] = true
		}
	}
	out := &asterismv1.Graph{}
	projects := map[string]bool{}
	for _, n := range g.GetNodes() {
		if keep[n.GetId()] {
			out.Nodes = append(out.Nodes, n)
			projects[n.GetProjectId()] = true
		}
	}
	for _, e := range g.GetEdges() {
		if keep[e.GetSourceId()] && keep[e.GetTargetId()] {
			out.Edges = append(out.Edges, e)
		}
	}
	for _, p := range g.GetProjects() {
		if projects[p.GetId()] {
			out.Projects = append(out.Projects, p)
		}
	}
	for _, r := range g.GetIngressRoutes() {
		if keep[r.GetProxyWorkloadId()] {
			out.IngressRoutes = append(out.IngressRoutes, r)
		}
	}
	return out, nil
}

// roleRank orders siblings from the edge inwards.
func roleRank(r asterismv1.WorkloadRole) int {
	switch r {
	case asterismv1.WorkloadRole_WORKLOAD_ROLE_PROXY:
		return 0
	case asterismv1.WorkloadRole_WORKLOAD_ROLE_APP:
		return 1
	case asterismv1.WorkloadRole_WORKLOAD_ROLE_WORKER:
		return 2
	case asterismv1.WorkloadRole_WORKLOAD_ROLE_QUEUE:
		return 3
	case asterismv1.WorkloadRole_WORKLOAD_ROLE_CACHE:
		return 4
	case asterismv1.WorkloadRole_WORKLOAD_ROLE_DATABASE:
		return 5
	case asterismv1.WorkloadRole_WORKLOAD_ROLE_STORAGE:
		return 6
	default:
		return 7
	}
}

func renderTopo(w io.Writer, th theme, g *asterismv1.Graph) {
	if len(g.GetNodes()) == 0 {
		fmt.Fprintln(w, th.dim("The topology is empty."))
		return
	}
	nodes := map[string]*asterismv1.TopologyNode{}
	for _, n := range g.GetNodes() {
		nodes[n.GetId()] = n
	}
	order := func(a, b *asterismv1.TopologyNode) int {
		return cmp.Or(cmp.Compare(roleRank(a.GetRole()), roleRank(b.GetRole())), cmp.Compare(a.GetLabel(), b.GetLabel()), cmp.Compare(a.GetId(), b.GetId()))
	}
	out := map[string][]*asterismv1.TopologyEdge{}
	incoming := map[string]int{}
	for _, e := range g.GetEdges() {
		if nodes[e.GetSourceId()] == nil || nodes[e.GetTargetId()] == nil {
			continue
		}
		out[e.GetSourceId()] = append(out[e.GetSourceId()], e)
		incoming[e.GetTargetId()]++
	}
	for id := range out {
		slices.SortStableFunc(out[id], func(a, b *asterismv1.TopologyEdge) int {
			return order(nodes[a.GetTargetId()], nodes[b.GetTargetId()])
		})
	}
	sorted := slices.Clone(g.GetNodes())
	slices.SortStableFunc(sorted, order)

	printed := map[string]bool{}
	var walk func(id, prefix string, onPath map[string]bool)
	walk = func(id, prefix string, onPath map[string]bool) {
		edges := out[id]
		for i, e := range edges {
			last := i == len(edges)-1
			branch, next := "├─▶ ", "│   "
			if last {
				branch, next = "└─▶ ", "    "
			}
			child := nodes[e.GetTargetId()]
			line := prefix + th.dim(branch)
			switch {
			case onPath[child.GetId()]:
				line += th.bold(clean(child.GetLabel())) + th.dim(" (cycle)")
			case printed[child.GetId()]:
				line += th.bold(clean(child.GetLabel())) + th.dim(" (see above)")
			default:
				line += nodeText(th, child)
			}
			if lbl := edgeText(e); lbl != "" {
				line += "  " + th.dim("via "+lbl)
			}
			fmt.Fprintln(w, line)
			if onPath[child.GetId()] || printed[child.GetId()] {
				continue
			}
			printed[child.GetId()] = true
			onPath[child.GetId()] = true
			walk(child.GetId(), prefix+th.dim(next), onPath)
			delete(onPath, child.GetId())
		}
	}
	root := func(n *asterismv1.TopologyNode) {
		fmt.Fprintln(w, nodeText(th, n))
		printed[n.GetId()] = true
		walk(n.GetId(), "", map[string]bool{n.GetId(): true})
		fmt.Fprintln(w)
	}
	// Entry points first, then anything only reachable through a cycle.
	for _, n := range sorted {
		if incoming[n.GetId()] == 0 && len(out[n.GetId()]) > 0 {
			root(n)
		}
	}
	for _, n := range sorted {
		if !printed[n.GetId()] && len(out[n.GetId()]) > 0 {
			root(n)
		}
	}
	var lone []*asterismv1.TopologyNode
	for _, n := range sorted {
		if !printed[n.GetId()] {
			lone = append(lone, n)
		}
	}
	if len(lone) > 0 {
		fmt.Fprintln(w, th.header("UNCONNECTED"))
		for _, n := range lone {
			fmt.Fprintln(w, "  "+nodeText(th, n))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, th.dim(fmt.Sprintf("%s · %s", plural(len(g.GetNodes()), "node", "nodes"), plural(len(g.GetEdges()), "edge", "edges"))))
}

func nodeText(th theme, n *asterismv1.TopologyNode) string {
	s := th.bold(clean(n.GetLabel()))
	if role := roleName(n.GetRole()); role != "" {
		s += " " + th.accent("["+role+"]")
	}
	if n.GetReplicas() > 1 {
		s += fmt.Sprintf(" ×%d", n.GetReplicas())
	}
	var facts []string
	if p := short(n.GetProjectId()); p != "" {
		facts = append(facts, clean(p))
	}
	facts = append(facts, th.health(n.GetStatus()), pctText(n.GetCpuUtil()), bytesText(n.GetMemUsedBytes()))
	s += "  " + strings.Join(facts, th.dim(" · "))
	if hosts := n.GetHostnames(); len(hosts) > 0 {
		hs := make([]string, 0, len(hosts))
		for _, h := range hosts[:min(len(hosts), 3)] {
			hs = append(hs, clean(h))
		}
		if len(hosts) > 3 {
			hs = append(hs, fmt.Sprintf("+%d", len(hosts)-3))
		}
		s += "  " + th.accent(strings.Join(hs, ", "))
	}
	return s
}

func edgeText(e *asterismv1.TopologyEdge) string {
	if l := clean(e.GetLabel()); l != "" {
		return l
	}
	if e.GetPort() > 0 {
		return strings.TrimSpace(fmt.Sprintf("%s :%d", e.GetProto(), e.GetPort()))
	}
	return ""
}
