const maximumRoomID = 18_446_744_073_709_551_615n;

export function validHostedRoomID(value: string): boolean {
  if (!/^[1-9][0-9]{0,19}$/.test(value)) return false;
  try { return BigInt(value) <= maximumRoomID; } catch { return false; }
}
