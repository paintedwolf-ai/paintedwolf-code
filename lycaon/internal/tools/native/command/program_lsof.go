package command

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// PortInventory reads host listeners and the processes holding them, marking
// the ones a session's launches own.
type PortInventory interface {
	PortHolders(context.Context, string, lineage.ListenerQuery) ([]hostprocess.PortHolder, error)
}

// maxJobAncestry bounds the parent walk from a listener to the job that started it.
const maxJobAncestry = 64

// lsofBuiltin answers `lsof [-nPt] [-i :PORT|-iTCP:PORT|-iUDP:PORT]
// [-sTCP:LISTEN]`. The session's own listeners are always listed with their
// job identity. For a named port, listeners held outside the session are
// listed only after the host process inspection approval process_list uses;
// without it the result says they were not inspected. Status 1 means no row.
func lsofBuiltin(spec toolcontract.CommandSpecification, sessionID string, jobs *SessionJobs, ports PortInventory, review tools.ProcessReviewer) jobBuiltin {
	return func(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(spec, request.Argv)
		if err != nil {
			return refusedResult("COMMAND_OPERAND_INVALID"), argvRejection(spec, err)
		}
		if len(bound.Operands()) != 0 {
			return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "lsof", "reason": "takes no operands"})
		}
		var query lineage.ListenerQuery
		if option, ok := bound.Last("-i"); ok {
			if query, err = lsofEndpoint(option.Values[0].Value); err != nil {
				return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "lsof", "word": option.Values[0].Value, "expected": ":PORT, TCP:PORT or UDP:PORT"})
			}
		}
		if option, ok := bound.Last("-s"); ok && !strings.EqualFold(option.Values[0].Value, "TCP:LISTEN") {
			return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "lsof", "word": option.Values[0].Value, "expected": "TCP:LISTEN"})
		}
		if ports == nil {
			err := errors.New("host listener inventory is not configured")
			return ownerFailure(err), err
		}
		holders, err := ports.PortHolders(ctx, sessionID, query)
		if err != nil {
			return ownerFailure(err), err
		}
		shown := slices.DeleteFunc(slices.Clone(holders), func(h hostprocess.PortHolder) bool { return h.Process == nil || !h.Process.Owned })
		uninspected := ""
		switch {
		case len(shown) == len(holders):
		case query.Port == 0:
			uninspected = "lsof: only this session's listeners are listed; name a port with -i :PORT to include other processes\n"
		case review == nil:
			uninspected = fmt.Sprintf("lsof: port %d is held outside this session; host process inspection is unavailable here, so those holders were not inspected\n", query.Port)
		default:
			err := review(ctx, "list", nil)
			if err == nil {
				shown = holders
				break
			}
			if toolrejection.AsToolReject(err) == nil {
				return ownerFailure(err), err
			}
			uninspected = fmt.Sprintf("lsof: port %d is held outside this session; host process inspection was not approved, so those holders were not inspected\n", query.Port)
		}
		var out strings.Builder
		if len(bound.Options("-t")) > 0 {
			writeLsofPIDs(&out, shown)
		} else {
			writeLsofRows(&out, shown, jobIdentities(ctx, jobs))
		}
		if _, err := fmt.Fprint(nonNilWriter(streams.Output), out.String()); err != nil {
			return ownerFailure(err), err
		}
		if _, err := fmt.Fprint(nonNilWriter(streams.Error), uninspected); err != nil {
			return ownerFailure(err), err
		}
		if out.Len() == 0 {
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
		}
		return readResult(0), nil
	}
}

// lsofEndpoint reads -i's [PROTOCOL]:PORT; lsof names protocols in any case.
func lsofEndpoint(endpoint string) (lineage.ListenerQuery, error) {
	name, value, ok := strings.Cut(endpoint, ":")
	port, err := strconv.Atoi(value)
	if !ok || err != nil || port < 1 || port > 65535 {
		return lineage.ListenerQuery{}, fmt.Errorf("invalid lsof endpoint %q", endpoint)
	}
	query := lineage.ListenerQuery{Port: uint16(port)}
	switch strings.ToLower(name) {
	case "":
	case "tcp":
		query.Protocols = []lineage.Protocol{lineage.ProtocolTCP}
	case "udp":
		query.Protocols = []lineage.Protocol{lineage.ProtocolUDP}
	default:
		return lineage.ListenerQuery{}, fmt.Errorf("invalid lsof protocol %q", name)
	}
	return query, nil
}

// jobIdentity names the session job a listener's process belongs to.
type jobIdentity struct{ number, id int }

// jobIdentities maps each live job's process to its job identity.
func jobIdentities(ctx context.Context, jobs *SessionJobs) map[int]jobIdentity {
	out := map[int]jobIdentity{}
	statuses, err := jobs.List(ctx)
	if err != nil {
		return out
	}
	for _, job := range statuses {
		if job.PID > 0 {
			out[job.PID] = jobIdentity{number: jobs.Number(job.Handle), id: jobs.publicID(job.Handle)}
		}
	}
	return out
}

// owningJob walks from a session-owned holder to the job process that started
// it; a job's server often listens from a child of the launched process.
func owningJob(process hostprocess.Process, jobs map[int]jobIdentity) (jobIdentity, bool) {
	for range maxJobAncestry {
		if job, ok := jobs[process.PID]; ok {
			return job, true
		}
		if process.ParentPID <= 1 {
			break
		}
		parent, err := hostprocess.Observe(process.ParentPID)
		if err != nil {
			break
		}
		process = parent
	}
	return jobIdentity{}, false
}

func writeLsofPIDs(out *strings.Builder, holders []hostprocess.PortHolder) {
	seen := map[int]bool{}
	for _, holder := range holders {
		if holder.Process == nil || seen[holder.Process.PID] {
			continue
		}
		seen[holder.Process.PID] = true
		fmt.Fprintf(out, "%d\n", holder.Process.PID)
	}
}

// writeLsofRows prints lsof's COMMAND PID USER TYPE NAME columns. A holder that
// could not be read keeps its port and owner with "?" for the process.
func writeLsofRows(out *strings.Builder, holders []hostprocess.PortHolder, jobs map[int]jobIdentity) {
	if len(holders) == 0 {
		return
	}
	table := tabwriter.NewWriter(out, 0, 8, 1, ' ', 0)
	fmt.Fprintln(table, "COMMAND\tPID\tUSER\tTYPE\tNAME")
	for _, holder := range holders {
		socket := holder.Socket
		command, pid, user := "?", "?", strconv.FormatUint(uint64(socket.UID), 10)
		name := strings.ToUpper(string(socket.Protocol)) + " " + lsofAddress(socket.Address, socket.Port)
		if socket.Protocol == lineage.ProtocolTCP {
			name += " (LISTEN)"
		}
		if process := holder.Process; process != nil {
			pid, user = strconv.Itoa(process.PID), strconv.FormatUint(uint64(process.UID), 10)
			if process.Name != "" {
				command = process.Name
			}
			if process.Owned {
				if job, ok := owningJob(*process, jobs); ok {
					name += fmt.Sprintf(" [session job #%d %d]", job.number, job.id)
				} else {
					name += " [session]"
				}
			}
		}
		family := "IPv6"
		if socket.Address.Is4() {
			family = "IPv4"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", command, pid, user, family, name)
	}
	_ = table.Flush()
}

// lsofAddress prints an endpoint the way lsof -n does: * for any address.
func lsofAddress(addr netip.Addr, port int) string {
	if addr.IsUnspecified() {
		return "*:" + strconv.Itoa(port)
	}
	return netip.AddrPortFrom(addr, uint16(port)).String()
}
