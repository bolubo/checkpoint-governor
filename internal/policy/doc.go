// Package policy is the algorithm layer. It is deliberately isolated: it imports only the
// standard library, reads nothing, and writes nothing.
//
// The implementation layer turns the world (directories, pods, job groups, storage) into a
// Snapshot, calls Decide, and carries out the Decision. Everything the algorithm needs is in the
// Snapshot; everything it decides is in the Decision. See "The line between the algorithm and the
// implementation" in design.
//
// The rules that keep the isolation real:
//
//   - the package imports only the standard library (enforced by a test);
//   - no clock, no disk, no network, no cluster;
//   - the job group shape (one pod, a gang, a batch) is normalised by the implementation before
//     it arrives; the algorithm only sees an opaque unit identifier;
//   - the algorithm may be replaced by a simpler one at any time, because it is one function
//     behind the Policy interface.
//
// The arithmetic is described on the policy page.
package policy
