# Icon provenance

WPX embeds its interface icons in `internal/web/templates/icons.html`. There is
no runtime CDN request and no icon package dependency. Every named template is
decorative by default (`aria-hidden="true"`); the surrounding control supplies
its accessible name.

## Lucide

The stroke icons come from the official
[`lucide-icons/lucide`](https://github.com/lucide-icons/lucide) repository,
release `1.27.0`, commit
`4aec3f892fd6c23063bc2fead83c899b5d412b1c`. Source files are under `icons/`
with the same basename as each template after the `icon-` prefix. WPX removes
fixed pixel dimensions, adds its fixed `size-4 shrink-0` classes and
`aria-hidden`, and consistently renders the upstream geometry with
`stroke-width="2"`, round caps, and round joins.

Lucide is licensed under ISC, with some icons derived from Feather under MIT.
The complete upstream notice is retained in [LUCIDE.txt](licenses/LUCIDE.txt).

The embedded Lucide names are: `activity`, `archive`, `bell`, `chevron-down`,
`chevron-left`, `clipboard`, `clock`, `copy`, `database`, `download`,
`ellipsis`, `folder`, `globe`, `hard-drive`, `info`, `layout-dashboard`,
`log-out`, `monitor`, `monitor-cog`, `moon`, `panels-top-left`, `pencil`,
`plus`, `refresh-cw`, `scissors`, `search`, `server`, `settings`, `shield`,
`sun`, `trash-2`, `upload`, `user`, `users`, and `x`.

## WordPress

`icon-wordpress` is the official W mark from the WordPress project's
[`WordPress/dashicons`](https://github.com/WordPress/dashicons) repository,
commit `628951563b9c0f0d293af8e40c9b0b3da5e2880d`, source
`sources/svg/wordpress.svg`. The path data and `0 0 20 20` view box are
preserved. WPX changes presentation to `fill="currentColor"`, adds the shared
size classes, and marks it decorative.

Dashicons is GPL-3.0 licensed, matching WPX's GPL-3.0 license. WordPress also
publishes its [official logo guidance](https://wordpress.org/about/logos/) and
[trademark policy](https://wordpressfoundation.org/trademark-policy/). The mark
is used only to identify WordPress workloads.
