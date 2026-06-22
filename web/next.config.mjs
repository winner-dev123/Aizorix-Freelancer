/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  poweredByHeader: false,
  // typedRoutes (experimental) rejects legitimate dynamic-navigation targets (e.g. a redirect
  // path read from a ?next= query param, variable hrefs) without casts scattered everywhere.
  // It is off by default in Next and purely a compile-time aid, so we leave it off.
  images: {
    remotePatterns: [
      // S3 / CloudFront-served avatars and (signed) screenshot thumbnails.
      { protocol: 'https', hostname: '*.amazonaws.com' },
      { protocol: 'https', hostname: '*.cloudfront.net' },
      { protocol: 'http', hostname: 'localhost' },
    ],
  },
  async rewrites() {
    // Proxy API calls in dev so the browser talks same-origin and httpOnly
    // refresh cookies flow without CORS/SameSite headaches.
    const gateway = process.env.API_GATEWAY_URL ?? 'http://localhost:8080';
    return [
      {
        source: '/api/gateway/:path*',
        destination: `${gateway}/:path*`,
      },
    ];
  },
};

export default nextConfig;
