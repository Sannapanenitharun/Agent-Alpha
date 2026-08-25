import { NextRequest, NextResponse } from 'next/server';

// The intake token is a write credential: it authenticates POST /v1/intake.
// It is read here, on the server, and never sent to the browser. Naming it
// NEXT_PUBLIC_* would inline it into the client bundle for anyone to read.
const intakeUrl = process.env.SIGNAL_API_URL ?? 'http://127.0.0.1:8080';
const intakeToken = process.env.SIGNAL_API_TOKEN ?? '';

// Only read-only query endpoints are reachable through the proxy, so a leaked
// dashboard session cannot be used to write telemetry.
const allowedPaths = new Set(['summary', 'telemetry']);

export const dynamic = 'force-dynamic';

export async function GET(request: NextRequest, context: { params: Promise<{ path: string[] }> }) {
  const { path } = await context.params;
  const target = path.join('/');
  if (!allowedPaths.has(target)) {
    return NextResponse.json({ error: 'unknown endpoint' }, { status: 404 });
  }
  if (!intakeToken) {
    return NextResponse.json({ error: 'SIGNAL_API_TOKEN is not configured' }, { status: 500 });
  }

  const url = new URL(`/v1/${target}`, intakeUrl);
  const limit = request.nextUrl.searchParams.get('limit');
  if (limit) {
    url.searchParams.set('limit', limit);
  }

  try {
    const response = await fetch(url, {
      headers: { Authorization: `Bearer ${intakeToken}` },
      cache: 'no-store',
      signal: AbortSignal.timeout(10_000),
    });
    const body = await response.text();
    return new NextResponse(body, {
      status: response.status,
      headers: { 'Content-Type': 'application/json' },
    });
  } catch {
    return NextResponse.json({ error: 'intake is unreachable' }, { status: 502 });
  }
}
