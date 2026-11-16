export type Json = null | boolean | number | string | Json[] | { [key: string]: Json };
export type JsonObject = Record<string, Json>;
export type SettingValue = string | number | boolean;
export type BinaryLike = string | Uint8Array;
export type DigestAlgorithm = "md5" | "sha1" | "sha256" | "sha512";
export type DigestEncoding = "hex" | "base64" | "base64url";
export type AESAlgorithm = "aes-128-gcm" | "aes-256-gcm" | "aes-128-cbc" | "aes-256-cbc";
export type RSAPadding = "oaep-sha256" | "oaep-sha1" | "pkcs1v15";

// ---- hook return values ----

/** Return exactly one of request, result, or error. */
export type ProbeDecision =
  | { request: ProbeRequest; result?: never; error?: never }
  | { result: ProbeResult; request?: never; error?: never }
  | { error: string; request?: never; result?: never };

/** Return exactly one of request, result, or error. R is the kind's result type. */
export type SteppedDecision<R> =
  | { request: ProbeRequest; result?: never; error?: never }
  | { result: R; request?: never; error?: never }
  | { error: string; request?: never; result?: never };

/** What an admin picked for one input: an object (several when the input is multiple) or a parameter value. */
export type AutomationInputValue = AutomationEntity | AutomationEntity[] | string | number | boolean;

export type ModelListDecision = SteppedDecision<ModelListResult>;
export type CredentialCheckDecision = SteppedDecision<CredentialCheckResult>;
export type SiteDetectDecision = SteppedDecision<SiteDetectResult>;

export type ProtocolDecodeResult =
  | { model: string; error?: never }
  | { error: { status: number; code: string; message: string }; model?: never };

export type SignAlgorithm = "hmac-sha256" | "hmac-sha1" | "hmac-sha512";
export type SignEncoding = "hex" | "base64";

export interface ProtocolParsedResponse {
  /** Send the upstream body to the client unchanged. */
  passthrough?: boolean;
  status?: number;
  contentType?: string;
  body?: string;
  usage?: ProtocolUsage;
  error?: string;
}

// ---- host API v2 (globals) ----

declare global {
  const bytes: {
    fromUTF8(text: string): Uint8Array;
    toUTF8(data: Uint8Array): string;
    fromHex(hex: string): Uint8Array;
    toHex(data: Uint8Array): string;
    fromBase64(b64: string): Uint8Array;
    toBase64(data: Uint8Array): string;
    fromBase64URL(b64url: string): Uint8Array;
    toBase64URL(data: Uint8Array): string;
    concat(chunks: Uint8Array[]): Uint8Array;
    equal(a: Uint8Array, b: Uint8Array): boolean;
  };
  const encoding: {
    hexEncode(data: BinaryLike): string;
    hexDecode(hex: string): Uint8Array;
    base64Encode(data: BinaryLike): string;
    base64Decode(b64: string): Uint8Array;
    base64URLEncode(data: BinaryLike): string;
    base64URLDecode(b64url: string): Uint8Array;
    utf8Encode(text: string): Uint8Array;
    utf8Decode(data: Uint8Array): string;
  };
  const codec: {
    base64Encode(value: string): string;
    base64Decode(value: string): string;
    base64URLEncode(value: string): string;
    base64URLDecode(value: string): string;
  };
  const crypto: {
    hmacSHA256(key: string, message: string): string;
    sha256(data: BinaryLike, encoding?: DigestEncoding): string;
    md5(data: BinaryLike, encoding?: DigestEncoding): string;
    sha1(data: BinaryLike, encoding?: DigestEncoding): string;
    sha512(data: BinaryLike, encoding?: DigestEncoding): string;
    digestBytes(alg: DigestAlgorithm, data: BinaryLike): Uint8Array;
    hmac(alg: DigestAlgorithm, key: BinaryLike, data: BinaryLike, encoding?: DigestEncoding): string;
    hmacBytes(alg: DigestAlgorithm, key: BinaryLike, data: BinaryLike): Uint8Array;
    randomBytes(length: number): Uint8Array;
    aesEncrypt(alg: AESAlgorithm, key: BinaryLike, iv: BinaryLike, plaintext: BinaryLike, aad?: BinaryLike): Uint8Array;
    aesDecrypt(alg: AESAlgorithm, key: BinaryLike, iv: BinaryLike, ciphertext: BinaryLike, aad?: BinaryLike): Uint8Array;
    rsaEncrypt(publicKeyPEM: string, plaintext: BinaryLike, padding?: RSAPadding): Uint8Array;
  };
  const jwt: {
    signHS256(payload: Record<string, any>, secret: string): string;
    decodeHS256(token: string, secret: string): any;
  };
  const storage: {
    get(key: string): Json;
    set(key: string, value: Json): void;
    delete(key: string): boolean;
    keys(): string[];
  };
  const utils: { uuid(): string };
  const log: {
    debug(message: string, fields?: Record<string, any>): void;
    info(message: string, fields?: Record<string, any>): void;
    warn(message: string, fields?: Record<string, any>): void;
  };
}
