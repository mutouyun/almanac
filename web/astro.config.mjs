import { defineConfig } from 'astro/config';

import tailwind from '@astrojs/tailwind';

// Almanac frontend: static output, built into web/dist and embedded
// into the Go binary. Keep it minimal for the MVP.
export default defineConfig({
  output: 'static',

  build: {
    // Emit assets under a predictable folder for embedding.
    assets: 'assets',
  },

  integrations: [tailwind()],
});