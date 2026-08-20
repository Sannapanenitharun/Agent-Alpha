import type { Metadata } from 'next';
import './globals.css';

export const metadata: Metadata = {
  title: 'Signal | Observability',
  description: 'Metrics, logs, and traces for your production systems.',
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
