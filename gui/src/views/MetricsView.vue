<script setup lang="ts">
// Live Brutal sample from GET /api/metrics. The service records the
// recommendation and does not change the cap or the selected node.
import { onMounted, onUnmounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { getMetrics, type MetricsReport } from "@/api";
import { formatRate } from "@/lib/format";

defineOptions({ name: "MetricsView" });
const { t } = useI18n();
const report = ref<MetricsReport | null>(null);
const error = ref("");
let timer = 0;

async function load() {
  try {
    report.value = await getMetrics();
    error.value = "";
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

onMounted(() => {
  void load();
  timer = window.setInterval(() => void load(), 1000);
});
onUnmounted(() => window.clearInterval(timer));

function pct(n: number): string {
  return `${Math.round(n * 100)}%`;
}
</script>

<template>
  <div class="metrics">
    <p class="md3-body-medium metrics__note">{{ t("metrics.note") }}</p>
    <p v-if="error" class="md3-body-medium metrics__error">{{ error }}</p>
    <p v-else-if="!report" class="md3-body-medium">{{ t("metrics.empty") }}</p>
    <template v-else>
      <div class="metrics__grid">
        <v-card variant="flat" color="surface-container" rounded="xl">
          <v-card-item>
            <v-card-subtitle>{{ t("metrics.action") }}</v-card-subtitle>
            <v-card-title>{{ report.decision.action }}</v-card-title>
            <p class="md3-body-medium">{{ report.decision.reason }}</p>
          </v-card-item>
        </v-card>
        <v-card variant="flat" color="surface-container" rounded="xl">
          <v-card-item>
            <v-card-subtitle>{{ t("metrics.utilization") }}</v-card-subtitle>
            <v-card-title>{{ pct(report.decision.utilization) }}</v-card-title>
            <p class="md3-body-medium">
              {{ t("metrics.down") }} {{ formatRate(report.down_bps_10s) }}
            </p>
          </v-card-item>
        </v-card>
        <v-card variant="flat" color="surface-container" rounded="xl">
          <v-card-item>
            <v-card-subtitle>{{ t("metrics.probe") }}</v-card-subtitle>
            <v-card-title>
              {{ report.probe.ok ? t("metrics.ok") : t("metrics.bad") }}
              {{ report.probe.ttfb_ms }} ms
            </v-card-title>
            <p class="md3-body-medium">
              {{ t("metrics.fails") }} {{ report.probe.fails }}
              <template v-if="report.probe.failover">
                · {{ t("metrics.failover") }}
              </template>
            </p>
          </v-card-item>
        </v-card>
        <v-card variant="flat" color="surface-container" rounded="xl">
          <v-card-item>
            <v-card-subtitle>{{ t("metrics.capDown") }}</v-card-subtitle>
            <v-card-title>{{ formatRate(report.cap_down_bps) }}</v-card-title>
            <p class="md3-body-medium">
              {{ t("metrics.capUp") }} {{ formatRate(report.cap_up_bps) }}
            </p>
          </v-card-item>
        </v-card>
      </div>
      <v-table density="comfortable" class="metrics__table">
        <thead>
          <tr>
            <th>{{ t("metrics.tag") }}</th>
            <th>{{ t("metrics.down") }}</th>
            <th>{{ t("metrics.up") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, tag) in report.outbounds" :key="tag">
            <td>{{ tag }}</td>
            <td>{{ formatRate(row.down) }}</td>
            <td>{{ formatRate(row.up) }}</td>
          </tr>
          <tr>
            <td>{{ t("metrics.demand") }}</td>
            <td>{{ formatRate(report.demand_bps) }}</td>
            <td></td>
          </tr>
        </tbody>
      </v-table>
      <p v-if="report.error" class="md3-body-medium metrics__error">
        {{ report.error }}
      </p>
    </template>
  </div>
</template>

<style scoped>
.metrics {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 8px 0 24px;
}
.metrics__note {
  color: rgb(var(--v-theme-on-surface-variant));
  margin: 0;
}
.metrics__error {
  color: rgb(var(--v-theme-error));
  margin: 0;
}
.metrics__grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 12px;
}
.metrics__table {
  background: rgb(var(--v-theme-surface-container));
  border-radius: 16px;
}
</style>
