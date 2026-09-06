// stripKeyComment returns just the "<type> <blob>" of an SSH public key, dropping
// any trailing comment. Gopher-generated keys carry no comment; operator-uploaded
// ones may — and we don't want to surface or copy that comment out of the
// dashboard, so what you see and what you copy is the bare, canonical key.
export function stripKeyComment(pub: string): string {
  return pub.trim().split(/\s+/).slice(0, 2).join(' ')
}
