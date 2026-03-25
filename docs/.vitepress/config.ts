import { defineConfig } from "vitepress";

export default defineConfig({
  title: "PocketCI",
  description: "Local-first PocketCI runtime documentation",
  base: "/docs/",
  cleanUrls: false,
  themeConfig: {
    nav: [
      { text: "Guides", link: "/guides/" },
      { text: "Runtime API", link: "/runtime/" },
      { text: "Operations", link: "/operations/" },
      { text: "Drivers", link: "/drivers/" },
    ],
    sidebar: {
      "/runtime/": [
        { text: "Overview", link: "/runtime/" },
        { text: "runtime.run()", link: "runtime-run" },
        { text: "runtime.agent()", link: "runtime-agent" },
        { text: "Volumes", link: "volumes" },
      ],
      "/guides/": [
        { text: "Overview", link: "/guides/" },
        { text: "Run Pipelines", link: "run" },
        { text: "Webhooks", link: "webhooks" },
        { text: "MCP", link: "mcp" },
      ],
      "/operations/": [
        { text: "Overview", link: "/operations/" },
        { text: "Authentication", link: "authentication" },
        { text: "Authorization (RBAC)", link: "rbac" },
        { text: "Storage", link: "storage" },
        { text: "Secrets", link: "secrets" },
        { text: "Caching", link: "caching" },
        { text: "Feature Gates", link: "feature-gates" },
      ],
      "/cli/": [
        { text: "Overview", link: "/cli/" },
        { text: "Runner", link: "runner" },
        { text: "Server", link: "server" },
        { text: "Login", link: "login" },
        { text: "Pipeline Set", link: "pipeline-set" },
        { text: "Pipeline Run", link: "pipeline-run" },
        { text: "Pipeline Trigger", link: "pipeline-trigger" },
        { text: "Pipeline Rm", link: "pipeline-rm" },
        { text: "Pipeline Ls", link: "pipeline-ls" },
        { text: "Pipeline Pause", link: "pipeline-pause" },
        { text: "Pipeline Unpause", link: "pipeline-unpause" },
      ],
      "/drivers/": [
        { text: "Overview", link: "/drivers/" },
        { text: "Native Resources", link: "native-resources" },
        { text: "Implementing Drivers", link: "implementing-driver" },
      ],
    },
    socialLinks: [
      { icon: "github", link: "https://github.com/jtarchie/pocketci" },
    ],
  },
});
