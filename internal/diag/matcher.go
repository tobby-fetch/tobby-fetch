// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package diag

import (
	"bytes"
	"sort"
)

// matcher finds every known secret form in one pass over a member: an
// Aho-Corasick automaton built once per bundle over the forms, shared by
// Find and Scrub. The previous search ran one bytes.ReplaceAll per form —
// thousands for a credentials file, each copying the member whether or not
// it matched — so its cost was O(forms × size); this one is O(size +
// matches).
//
// A form is entered into the automaton by its first triggerLen bytes only
// (its trigger), and a trigger hit is confirmed against the whole form: a
// one-megabyte secret file is one form, and an automaton over its every
// byte would cost gigabytes. The trigger is also what finds a long form
// cut by a truncation: a hit whose confirmation runs into a cut, with what
// precedes the cut a prefix of the form, is a leak of that prefix.
type matcher struct {
	nodes []acNode
	// keys[k] lists the forms whose trigger is key k.
	keys   [][]int
	keyLen []int
	forms  [][]byte
	// bounded marks the short forms, matched only between non-word bytes.
	bounded []bool
	// byPrefix indexes the forms by their first four bytes (minSecret).
	byPrefix map[[4]byte][]int
}

const triggerLen = 32

type acEdge struct {
	b    byte
	next int32
}

type acNode struct {
	edges []acEdge // sorted by b
	fail  int32
	// dict is the nearest node on the failure chain that ends a key, or -1.
	dict int32
	// key ends at this node, or -1.
	key int32
}

func (n *acNode) child(b byte) int32 {
	i := sort.Search(len(n.edges), func(i int) bool { return n.edges[i].b >= b })
	if i < len(n.edges) && n.edges[i].b == b {
		return n.edges[i].next
	}
	return -1
}

func newMatcher(forms [][]byte, bounded []bool) *matcher {
	m := &matcher{
		forms: forms, bounded: bounded, byPrefix: map[[4]byte][]int{},
		nodes: []acNode{{fail: 0, dict: -1, key: -1}},
	}
	keyOf := map[string]int{}
	for i, f := range forms {
		if len(f) >= 4 {
			var p4 [4]byte
			copy(p4[:], f)
			m.byPrefix[p4] = append(m.byPrefix[p4], i)
		}
		trig := f
		if len(trig) > triggerLen {
			trig = trig[:triggerLen]
		}
		k, ok := keyOf[string(trig)]
		if !ok {
			k = len(m.keys)
			keyOf[string(trig)] = k
			m.keys = append(m.keys, nil)
			m.keyLen = append(m.keyLen, len(trig))
			m.insert(trig, k)
		}
		m.keys[k] = append(m.keys[k], i)
	}
	m.link()
	return m
}

func (m *matcher) insert(key []byte, k int) {
	cur := int32(0)
	for _, b := range key {
		nx := m.nodes[cur].child(b)
		if nx < 0 {
			nx = int32(len(m.nodes)) //nolint:gosec // G115: at most triggerLen nodes per form, far below 2^31
			m.nodes = append(m.nodes, acNode{dict: -1, key: -1})
			n := &m.nodes[cur]
			i := sort.Search(len(n.edges), func(i int) bool { return n.edges[i].b >= b })
			n.edges = append(n.edges, acEdge{})
			copy(n.edges[i+1:], n.edges[i:])
			n.edges[i] = acEdge{b: b, next: nx}
		}
		cur = nx
	}
	m.nodes[cur].key = int32(k) //nolint:gosec // G115: at most one key per form
}

// link computes the failure and dictionary links, breadth first.
func (m *matcher) link() {
	queue := make([]int32, 0, len(m.nodes))
	for _, e := range m.nodes[0].edges {
		m.nodes[e.next].fail = 0
		queue = append(queue, e.next)
	}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, e := range m.nodes[u].edges {
			v := e.next
			f := m.nodes[u].fail
			for f != 0 && m.nodes[f].child(e.b) < 0 {
				f = m.nodes[f].fail
			}
			if c := m.nodes[f].child(e.b); c >= 0 && c != v {
				f = c
			} else {
				f = 0
			}
			m.nodes[v].fail = f
			if m.nodes[f].key >= 0 {
				m.nodes[v].dict = f
			} else {
				m.nodes[v].dict = m.nodes[f].dict
			}
			queue = append(queue, v)
		}
	}
}

// hit is one occurrence of a form in a member, [start, end). A partial hit
// is a prefix of the form that runs into a cut of the member: what a
// truncation left of a secret.
type hit struct {
	start, end int
	partial    bool
}

// scan reports every occurrence of a form in data, and every prefix of a
// form that ends exactly at one of cuts (sorted offsets into data where a
// truncation happened) and is at least minPartial bytes long. fn returns
// false to stop the scan.
func (m *matcher) scan(data []byte, cuts []int, minPartial int, fn func(hit) bool) {
	if m == nil || len(m.forms) == 0 {
		return
	}
	state := int32(0)
	for i := range data {
		c := data[i]
		for state != 0 && m.nodes[state].child(c) < 0 {
			state = m.nodes[state].fail
		}
		if nx := m.nodes[state].child(c); nx >= 0 {
			state = nx
		}
		for o := state; o > 0; o = m.nodes[o].dict {
			if k := m.nodes[o].key; k >= 0 {
				start := i + 1 - m.keyLen[k]
				for _, fi := range m.keys[k] {
					if h, ok := m.confirm(data, start, fi, cuts, minPartial); ok && !fn(h) {
						return
					}
				}
			}
		}
	}
	// Prefixes shorter than a trigger: the automaton cannot see them, so
	// each start a few bytes before a cut is looked up by its first four
	// bytes and compared directly.
	for _, cut := range cuts {
		for s := max(0, cut-(triggerLen-1)); s+minPartial <= cut; s++ {
			var p4 [4]byte
			copy(p4[:], data[s:s+4])
			for _, fi := range m.byPrefix[p4] {
				f := m.forms[fi]
				if k := cut - s; k < len(f) && k < triggerLen && bytes.Equal(data[s:cut], f[:k]) {
					if !fn(hit{start: s, end: cut, partial: true}) {
						return
					}
				}
			}
		}
	}
}

// confirm checks the whole form fi at start once its trigger matched.
func (m *matcher) confirm(data []byte, start, fi int, cuts []int, minPartial int) (hit, bool) {
	f := m.forms[fi]
	end := start + len(f)
	if end <= len(data) && bytes.Equal(data[start:end], f) {
		if m.bounded[fi] && ((start > 0 && isWordByte(data[start-1])) || (end < len(data) && isWordByte(data[end]))) {
			return hit{}, false
		}
		return hit{start: start, end: end}, true
	}
	// The form did not fit: is a cut where it stops matching?
	j := sort.SearchInts(cuts, start+1)
	if j == len(cuts) {
		return hit{}, false
	}
	cut := cuts[j]
	if cut-start >= minPartial && cut-start < len(f) && cut <= len(data) && bytes.Equal(data[start:cut], f[:cut-start]) {
		return hit{start: start, end: cut, partial: true}, true
	}
	return hit{}, false
}

// suffixAtStart reports the longest suffix of a form, at least minPartial
// bytes, that data begins with: what a tail cut left of a secret.
func (m *matcher) suffixAtStart(data []byte, minPartial int) int {
	if m == nil {
		return 0
	}
	best := 0
	for _, f := range m.forms {
		for k := min(len(f)-1, len(data)); k >= minPartial && k > best; k-- {
			if data[0] == f[len(f)-k] && bytes.Equal(data[:k], f[len(f)-k:]) {
				best = k
				break
			}
		}
	}
	return best
}
