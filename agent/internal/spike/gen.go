// Package spike holds the byte-accounting feasibility experiment (P4-A22).
//
// Separate from internal/collector on purpose: the shipped capture path must not be destabilised
// while an open question is being answered. Whatever this concludes, the experiment and its
// result are the Phase 4 deliverable (ADR-001 §9).
package spike

//go:generate go tool bpf2go -cc clang -target amd64 -type close_event -cflags "-O2 -g -Wall -Werror -I/usr/include/x86_64-linux-gnu" TcpBytes ../../bpf/tcp_bytes_spike.bpf.c
