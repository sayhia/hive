<script setup lang="ts">
// A styled native <select>. Ported from papr's `Select` helper. papr's was
// generic over the option value type; here it is plain `string` (callers that
// need a narrower type cast the emitted value), which keeps it compatible with
// the project's older vue-tsc and matches how AddFeedDialog.vue handles selects.

defineProps<{
  value: string;
  options: { value: string; label: string }[];
  ariaLabel?: string;
}>();

const emit = defineEmits<{
  (e: "change", value: string): void;
}>();
</script>

<template>
  <select
    class="s-select"
    :value="value"
    :aria-label="ariaLabel"
    @change="emit('change', ($event.target as HTMLSelectElement).value)"
  >
    <option v-for="o in options" :key="o.value" :value="o.value">
      {{ o.label }}
    </option>
  </select>
</template>
