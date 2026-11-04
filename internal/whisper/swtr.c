// Обёртки над whisper.cpp для whisper.go: тишина в логах и распознавание с остановкой.
#include "whisper.h"
#include "swtr.h"

extern _Bool swtrAbort(void * user);

static void swtr_quiet(enum ggml_log_level level, const char * text, void * user) {}

static _Bool swtr_abort(void * user) { return swtrAbort(user); }

void swtr_init(void) { whisper_log_set(swtr_quiet, NULL); }

int swtr_full(struct whisper_context * ctx, const float * samples, int n, const char * lang, int threads, int timestamps, int audio_ctx, void * handle) {
	struct whisper_full_params p = whisper_full_default_params(WHISPER_SAMPLING_GREEDY);
	p.n_threads = threads;
	p.language = lang;
	p.detect_language = 0;
	p.translate = 0;
	p.no_timestamps = !timestamps;
	p.audio_ctx = audio_ctx; // 0 — всё окно в 30 секунд
	// без повторных попыток «погорячее»: на звуке без слов Whisper иначе перебирает до пяти расшифровок,
	// а выдумки на музыке мы и так отсекаем
	p.temperature_inc = 0.0f;
	p.token_timestamps = timestamps; // время каждого слова: начало и конец фразы точнее
	p.print_progress = 0;
	p.print_realtime = 0;
	p.print_timestamps = 0;
	p.print_special = 0;
	p.suppress_blank = 1;
	p.abort_callback = swtr_abort;
	p.abort_callback_user_data = handle;
	return whisper_full(ctx, p, samples, n);
}
