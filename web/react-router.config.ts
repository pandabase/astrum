import type { Config } from "@react-router/dev/config";

export default {
  // The Go server serves the built index.html for every client route; see WEB_DIR.
  ssr: false,
} satisfies Config;
