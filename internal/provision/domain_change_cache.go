package provision

// A database rewrite bypasses WordPress's object-cache invalidation, including
// posts, metadata and plugin-defined groups. A global Redis flush would clear
// neighboring sites. Only the managed integration's exact immutable-site prefix
// is eligible; an expert-replaced cache backend must be handled explicitly.
func domainWordPressCacheGuard(siteID string) string {
	return " if (wp_using_ext_object_cache()) { global $wp_object_cache;" +
		" if (!defined('WP_REDIS_PREFIX') || WP_REDIS_PREFIX !== 'wpx:" + siteID + ":' || !method_exists($wp_object_cache, 'redis_instance') || !($wp_object_cache->redis_instance() instanceof Redis)) {" +
		" WP_CLI::error('Domain changes require the managed PhpRedis cache with this site-specific WP_REDIS_PREFIX, or no persistent object cache.'); } }"
}

func domainClearSiteCache(siteID string) string {
	// Use the integration's existing connection, including its configured DB
	// and credentials. SCAN proceeds incrementally; bounded UNLINK batches free
	// values without blocking Redis on their size. No global cache flush is used.
	return domainWordPressCacheGuard(siteID) + `
if (wp_using_ext_object_cache()) {
    $redis = $wp_object_cache->redis_instance();
    $prefix = WP_REDIS_PREFIX;
    $cursor = '0';
    $deadline = microtime(true) + 60;
    do {
        if (microtime(true) > $deadline) { WP_CLI::error('Site cache invalidation exceeded 60 seconds.'); }
        $batch = $redis->rawCommand('SCAN', $cursor, 'MATCH', $prefix . '*', 'COUNT', '500');
        if (!is_array($batch) || count($batch) !== 2 || !is_array($batch[1])) { WP_CLI::error('Site cache scan failed.'); }
        $cursor = (string) $batch[0];
        if (!ctype_digit($cursor)) { WP_CLI::error('Site cache scan returned an invalid cursor.'); }
        foreach (array_chunk($batch[1], 500) as $keys) {
            foreach ($keys as $key) {
                if (!is_string($key) || strpos($key, $prefix) !== 0) { WP_CLI::error('Site cache scan crossed its prefix boundary.'); }
            }
            if ($keys && $redis->rawCommand('UNLINK', ...$keys) === false) { WP_CLI::error('Site cache deletion failed.'); }
        }
    } while ($cursor !== '0');
}
if (function_exists('wp_cache_supports') && wp_cache_supports('flush_runtime')) { wp_cache_flush_runtime(); }
`
}
