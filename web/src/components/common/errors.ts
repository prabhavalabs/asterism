import { ConnectError } from "@connectrpc/connect";

/** Human-readable message for ConnectRPC errors, Errors and strings. */
export function errorMessage(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage || error.message;
  if (error instanceof Error) return error.message;
  if (typeof error === "string" && error) return error;
  return "Something went wrong. Try again in a moment.";
}
