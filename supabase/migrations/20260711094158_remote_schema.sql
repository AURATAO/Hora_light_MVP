


SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;


CREATE EXTENSION IF NOT EXISTS "pg_net" WITH SCHEMA "extensions";






COMMENT ON SCHEMA "public" IS 'standard public schema';



CREATE EXTENSION IF NOT EXISTS "http" WITH SCHEMA "extensions";






CREATE EXTENSION IF NOT EXISTS "pg_stat_statements" WITH SCHEMA "extensions";






CREATE EXTENSION IF NOT EXISTS "pgcrypto" WITH SCHEMA "extensions";






CREATE EXTENSION IF NOT EXISTS "supabase_vault" WITH SCHEMA "vault";






CREATE EXTENSION IF NOT EXISTS "uuid-ossp" WITH SCHEMA "extensions";






CREATE TYPE "public"."notification_type" AS ENUM (
    'ORDER_ACCEPTED',
    'CLOCK_IN',
    'CLOCK_OUT',
    'CANCELLED',
    'COMPLETED'
);


ALTER TYPE "public"."notification_type" OWNER TO "postgres";


CREATE OR REPLACE FUNCTION "public"."adjust_time"("p_task_id" "uuid", "p_minutes_delta" integer) RETURNS "void"
    LANGUAGE "plpgsql" SECURITY DEFINER
    SET "search_path" TO 'pg_catalog', 'public'
    AS $$
declare
  v_id uuid;
  v_start timestamptz;
  v_end timestamptz;
begin
  perform public.assert_ops_admin();

  select id, start_at, end_at
    into v_id, v_start, v_end
  from public.worklogs
  where task_id = p_task_id
  order by coalesce(end_at, start_at) desc
  limit 1;

  if v_id is null then
    raise exception 'No worklog found for this task';
  end if;

  if v_end is null then
    v_end := now();
  end if;

  update public.worklogs
     set end_at = v_end + make_interval(mins => p_minutes_delta),
         updated_at = now()
   where id = v_id;

  insert into public.audit_logs (job_id, actor_id, action, meta, reason)
  values (p_task_id, auth.uid(), 'TIME_ADJUSTED',
          jsonb_build_object('delta_minutes', p_minutes_delta), null);
end$$;


ALTER FUNCTION "public"."adjust_time"("p_task_id" "uuid", "p_minutes_delta" integer) OWNER TO "postgres";


CREATE OR REPLACE FUNCTION "public"."assert_ops_admin"() RETURNS "void"
    LANGUAGE "plpgsql" SECURITY DEFINER
    SET "search_path" TO 'pg_catalog', 'public'
    AS $$
begin
  if not public.is_ops_admin() then
    raise exception 'not authorized';
  end if;
end$$;


ALTER FUNCTION "public"."assert_ops_admin"() OWNER TO "postgres";


CREATE OR REPLACE FUNCTION "public"."cancel_task"("p_task_id" "uuid", "p_reason" "text" DEFAULT ''::"text") RETURNS "void"
    LANGUAGE "plpgsql" SECURITY DEFINER
    SET "search_path" TO 'pg_catalog', 'public'
    AS $$
begin
  perform public.assert_ops_admin();

  update public.worklogs
     set end_at = now(), updated_at = now()
   where task_id = p_task_id
     and end_at is null;

  update public.tasks
     set status = 'cancelled',
         cancelled_at = now(),
         cancel_reason = nullif(p_reason, '')
   where id = p_task_id;

  insert into public.audit_logs (job_id, actor_id, action, reason, meta)
  values (p_task_id, auth.uid(), 'CANCELLED', nullif(p_reason,''), '{}'::jsonb);
end$$;


ALTER FUNCTION "public"."cancel_task"("p_task_id" "uuid", "p_reason" "text") OWNER TO "postgres";


CREATE OR REPLACE FUNCTION "public"."current_user_id"() RETURNS "uuid"
    LANGUAGE "sql" STABLE
    SET "search_path" TO 'pg_catalog', 'public'
    AS $$
  select id from public.users where supabase_sub = auth.uid()
$$;


ALTER FUNCTION "public"."current_user_id"() OWNER TO "postgres";


CREATE OR REPLACE FUNCTION "public"."force_complete"("p_task_id" "uuid") RETURNS "void"
    LANGUAGE "plpgsql" SECURITY DEFINER
    SET "search_path" TO 'pg_catalog', 'public'
    AS $$
begin
  perform public.assert_ops_admin();

  update public.worklogs
     set end_at = now(), updated_at = now()
   where task_id = p_task_id
     and end_at is null;

  update public.tasks
     set status = 'completed'
   where id = p_task_id;

  insert into public.audit_logs (job_id, actor_id, action, reason, meta)
  values (p_task_id, auth.uid(), 'FORCE_COMPLETED', null, '{}'::jsonb);
end$$;


ALTER FUNCTION "public"."force_complete"("p_task_id" "uuid") OWNER TO "postgres";

SET default_tablespace = '';

SET default_table_access_method = "heap";


CREATE TABLE IF NOT EXISTS "public"."tasks" (
    "id" "uuid" DEFAULT "gen_random_uuid"() NOT NULL,
    "title" "text" NOT NULL,
    "description" "text" NOT NULL,
    "category" "text" NOT NULL,
    "location_text" "text" NOT NULL,
    "estimated_minutes" integer NOT NULL,
    "prepay_amount_cents" integer DEFAULT 0 NOT NULL,
    "is_immediate" boolean DEFAULT false NOT NULL,
    "scheduled_at" timestamp with time zone,
    "requester" "text" NOT NULL,
    "status" "text" DEFAULT 'open'::"text" NOT NULL,
    "assigned_to" "text" DEFAULT ''::"text" NOT NULL,
    "created_at" timestamp with time zone DEFAULT "now"() NOT NULL,
    "requester_id" "uuid" NOT NULL,
    "assigned_to_id" "uuid",
    "cancelled_at" timestamp with time zone,
    "cancel_reason" "text",
    "job_lat" double precision,
    "job_lng" double precision,
    "transport_required" "text" DEFAULT 'none'::"text" NOT NULL,
    "travel_time_minutes" integer,
    "total_estimate_minutes" integer,
    "completion_photo_url" "text",
    "completion_note" "text",
    "completed_at" timestamp with time zone,
    CONSTRAINT "tasks_category_check" CHECK (("category" = ANY (ARRAY['task'::"text", 'companion'::"text", 'quick_errand'::"text", 'standard'::"text", 'half_day'::"text", 'full_day'::"text", 'delivery'::"text", 'grocery'::"text", 'laundry'::"text", 'queue'::"text", 'anything_else'::"text", 'companionship'::"text"]))),
    CONSTRAINT "tasks_estimated_minutes_check" CHECK (("estimated_minutes" > 0)),
    CONSTRAINT "tasks_prepay_amount_cents_check" CHECK (("prepay_amount_cents" >= 0)),
    CONSTRAINT "tasks_status_check" CHECK (("status" = ANY (ARRAY['open'::"text", 'completed'::"text", 'cancelled'::"text"])))
);


ALTER TABLE "public"."tasks" OWNER TO "postgres";


CREATE TABLE IF NOT EXISTS "public"."users" (
    "id" "uuid" DEFAULT "gen_random_uuid"() NOT NULL,
    "google_sub" "text",
    "email" "text",
    "name" "text",
    "picture" "text",
    "created_at" timestamp with time zone DEFAULT "now"(),
    "supabase_sub" "uuid"
);


ALTER TABLE "public"."users" OWNER TO "postgres";


CREATE TABLE IF NOT EXISTS "public"."worklogs" (
    "id" "uuid" DEFAULT "gen_random_uuid"() NOT NULL,
    "task_id" "uuid" NOT NULL,
    "user" "text" NOT NULL,
    "start_at" timestamp with time zone NOT NULL,
    "end_at" timestamp with time zone,
    "created_at" timestamp with time zone DEFAULT "now"() NOT NULL,
    "updated_at" timestamp with time zone DEFAULT "now"() NOT NULL
);


ALTER TABLE "public"."worklogs" OWNER TO "postgres";


CREATE OR REPLACE VIEW "public"."view_ops_task_worklog_agg" AS
 SELECT "task_id",
    "min"("start_at") AS "first_start_at",
    "max"("end_at") AS "last_end_at",
    COALESCE("sum"(
        CASE
            WHEN ("end_at" IS NOT NULL) THEN ((EXTRACT(epoch FROM ("end_at" - "start_at")))::integer / 60)
            ELSE 0
        END), (0)::bigint) AS "total_minutes_done",
    COALESCE("sum"(
        CASE
            WHEN ("end_at" IS NULL) THEN ((EXTRACT(epoch FROM ("now"() - "start_at")))::integer / 60)
            ELSE 0
        END), (0)::bigint) AS "running_minutes",
    "max"(COALESCE("end_at", "start_at")) AS "last_wl_event_at"
   FROM "public"."worklogs" "w"
  GROUP BY "task_id";


ALTER VIEW "public"."view_ops_task_worklog_agg" OWNER TO "postgres";


CREATE OR REPLACE VIEW "public"."view_ops_tasks" AS
 SELECT "t"."id" AS "task_id",
    "t"."title",
    "t"."category",
    "t"."location_text",
    "t"."status",
    "t"."estimated_minutes",
    (("t"."prepay_amount_cents")::numeric / 100.0) AS "prepay_amount",
    "t"."is_immediate",
    "t"."scheduled_at",
    "t"."created_at",
    "t"."cancelled_at",
    "t"."cancel_reason",
    "r"."email" AS "requester_email",
    "s"."email" AS "supporter_email",
    "a"."first_start_at",
    "a"."last_end_at",
    "a"."total_minutes_done",
    "a"."running_minutes",
    (COALESCE("a"."total_minutes_done", (0)::bigint) + COALESCE("a"."running_minutes", (0)::bigint)) AS "duration_minutes",
    GREATEST("t"."created_at", COALESCE("a"."last_wl_event_at", "t"."created_at"), COALESCE("t"."cancelled_at", "t"."created_at")) AS "last_event_at"
   FROM ((("public"."tasks" "t"
     LEFT JOIN "public"."users" "r" ON (("r"."id" = "t"."requester_id")))
     LEFT JOIN "public"."users" "s" ON (("s"."id" = "t"."assigned_to_id")))
     LEFT JOIN "public"."view_ops_task_worklog_agg" "a" ON (("a"."task_id" = "t"."id")));


ALTER VIEW "public"."view_ops_tasks" OWNER TO "postgres";


CREATE OR REPLACE FUNCTION "public"."get_ops_feed"("p_status" "text" DEFAULT NULL::"text", "p_q" "text" DEFAULT NULL::"text") RETURNS SETOF "public"."view_ops_tasks"
    LANGUAGE "plpgsql" SECURITY DEFINER
    SET "search_path" TO 'pg_catalog', 'public'
    AS $$
begin
  perform public.assert_ops_admin();

  return query
  select v.*
  from public.view_ops_tasks v
  where (p_status is null or p_status = 'all' or v.status = p_status)
    and (
      p_q is null
      or p_q = ''
      or (v.title ilike '%'||p_q||'%'
          or v.location_text ilike '%'||p_q||'%'
          or v.requester_email ilike '%'||p_q||'%'
          or v.supporter_email ilike '%'||p_q||'%')
    )
  order by v.last_event_at desc
  limit 500;
end$$;


ALTER FUNCTION "public"."get_ops_feed"("p_status" "text", "p_q" "text") OWNER TO "postgres";


CREATE OR REPLACE FUNCTION "public"."handle_new_user"() RETURNS "trigger"
    LANGUAGE "plpgsql" SECURITY DEFINER
    SET "search_path" TO 'public'
    AS $$
begin
  insert into public.users (supabase_sub, email, name)
  values (
    new.id,  -- Supabase 的 auth.users.id
    new.email,
    coalesce(new.raw_user_meta_data->>'name', split_part(new.email,'@',1))
  )
  on conflict (supabase_sub) do nothing;
  return new;
end;
$$;


ALTER FUNCTION "public"."handle_new_user"() OWNER TO "postgres";


CREATE OR REPLACE FUNCTION "public"."is_ops_admin"() RETURNS boolean
    LANGUAGE "sql" STABLE
    SET "search_path" TO 'pg_catalog', 'public'
    AS $$
  select coalesce((auth.jwt() ->> 'email') in (
    'auratao.model@gmail.com',
    'liang.you@horaapp.co',
    'liang.you@arcodiax.com',
    'rollod4@gmail.com',
    'daniele@arcodiax.com'
  ), false)
$$;


ALTER FUNCTION "public"."is_ops_admin"() OWNER TO "postgres";


CREATE OR REPLACE FUNCTION "public"."notify_new_task"() RETURNS "trigger"
    LANGUAGE "plpgsql"
    AS $$
begin
  perform
    net.http_post(
      url := 'https://akxsdkerudurzcemurrb.supabase.co/functions/v1/notify-new-task',
      headers := jsonb_build_object(
        'Content-Type',  'application/json',
        'Authorization', 'Bearer ' || current_setting('app.service_role_key', true)
      ),
      body := jsonb_build_object('record', row_to_json(NEW))
    );
  return NEW;
end;
$$;


ALTER FUNCTION "public"."notify_new_task"() OWNER TO "postgres";


CREATE OR REPLACE FUNCTION "public"."whoami"() RETURNS "jsonb"
    LANGUAGE "sql" STABLE SECURITY DEFINER
    SET "search_path" TO 'pg_catalog', 'public'
    AS $$
  select jsonb_build_object(
    'uid', auth.uid(),
    'email', auth.jwt() ->> 'email'
  );
$$;


ALTER FUNCTION "public"."whoami"() OWNER TO "postgres";


CREATE TABLE IF NOT EXISTS "public"."audit_logs" (
    "id" "uuid" DEFAULT "gen_random_uuid"() NOT NULL,
    "job_id" "uuid" NOT NULL,
    "actor_id" "uuid" NOT NULL,
    "action" "text" NOT NULL,
    "reason" "text",
    "meta" "jsonb" DEFAULT '{}'::"jsonb" NOT NULL,
    "created_at" timestamp with time zone DEFAULT "now"() NOT NULL
);


ALTER TABLE "public"."audit_logs" OWNER TO "postgres";


CREATE TABLE IF NOT EXISTS "public"."messages" (
    "id" bigint NOT NULL,
    "task_id" bigint,
    "sender" "uuid" NOT NULL,
    "body" "text" NOT NULL,
    "created_at" timestamp with time zone DEFAULT "now"()
);


ALTER TABLE "public"."messages" OWNER TO "postgres";


CREATE SEQUENCE IF NOT EXISTS "public"."messages_id_seq"
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE "public"."messages_id_seq" OWNER TO "postgres";


ALTER SEQUENCE "public"."messages_id_seq" OWNED BY "public"."messages"."id";



CREATE TABLE IF NOT EXISTS "public"."notifications" (
    "id" "uuid" DEFAULT "gen_random_uuid"() NOT NULL,
    "user_id" "uuid" NOT NULL,
    "task_id" "uuid" NOT NULL,
    "type" "public"."notification_type" NOT NULL,
    "title" "text" NOT NULL,
    "body" "text" NOT NULL,
    "unread" boolean DEFAULT true NOT NULL,
    "via_email" boolean DEFAULT false NOT NULL,
    "email_sent_at" timestamp with time zone,
    "created_at" timestamp with time zone DEFAULT "now"() NOT NULL
);


ALTER TABLE "public"."notifications" OWNER TO "postgres";


CREATE TABLE IF NOT EXISTS "public"."profiles" (
    "email" "text" NOT NULL,
    "name" "text" DEFAULT ''::"text" NOT NULL,
    "phone" "text" DEFAULT ''::"text" NOT NULL,
    "city" "text" DEFAULT ''::"text" NOT NULL,
    "avatar_url" "text" DEFAULT ''::"text" NOT NULL,
    "bio" "text" DEFAULT ''::"text" NOT NULL,
    "created_at" timestamp with time zone DEFAULT "now"() NOT NULL,
    "updated_at" timestamp with time zone DEFAULT "now"() NOT NULL,
    "id" "uuid" DEFAULT "gen_random_uuid"() NOT NULL,
    "beta_accepted" boolean DEFAULT false NOT NULL,
    "is_verified_supporter" boolean DEFAULT false NOT NULL,
    "supporter_applied_at" timestamp with time zone,
    "supporter_status" "text" DEFAULT 'none'::"text" NOT NULL,
    CONSTRAINT "profiles_supporter_status_check" CHECK (("supporter_status" = ANY (ARRAY['none'::"text", 'applied'::"text", 'approved'::"text", 'rejected'::"text"])))
);


ALTER TABLE "public"."profiles" OWNER TO "postgres";


CREATE TABLE IF NOT EXISTS "public"."reviews" (
    "id" "uuid" DEFAULT "gen_random_uuid"() NOT NULL,
    "task_id" "uuid",
    "reviewer_id" "uuid",
    "supporter_id" "uuid",
    "stars" integer,
    "value_rating" "text",
    "would_rehire" boolean,
    "comment" "text",
    "created_at" timestamp with time zone DEFAULT "now"(),
    CONSTRAINT "reviews_stars_check" CHECK ((("stars" >= 1) AND ("stars" <= 5))),
    CONSTRAINT "reviews_value_rating_check" CHECK (("value_rating" = ANY (ARRAY['not_worth'::"text", 'fair'::"text", 'great'::"text"])))
);


ALTER TABLE "public"."reviews" OWNER TO "postgres";


CREATE TABLE IF NOT EXISTS "public"."task_gps_pings" (
    "id" "uuid" DEFAULT "gen_random_uuid"() NOT NULL,
    "task_id" "uuid",
    "user_id" "uuid",
    "lat" double precision NOT NULL,
    "lng" double precision NOT NULL,
    "accuracy" integer,
    "created_at" timestamp with time zone DEFAULT "now"()
);


ALTER TABLE "public"."task_gps_pings" OWNER TO "postgres";


CREATE TABLE IF NOT EXISTS "public"."timesheets" (
    "id" "uuid" DEFAULT "gen_random_uuid"() NOT NULL,
    "task_id" "uuid",
    "worker_id" "uuid",
    "type" "text",
    "lat" double precision,
    "lng" double precision,
    "accuracy_metres" double precision,
    "timestamp" timestamp with time zone,
    "created_at" timestamp with time zone DEFAULT "now"(),
    CONSTRAINT "timesheets_type_check" CHECK (("type" = ANY (ARRAY['in'::"text", 'out'::"text"])))
);


ALTER TABLE "public"."timesheets" OWNER TO "postgres";


ALTER TABLE ONLY "public"."messages" ALTER COLUMN "id" SET DEFAULT "nextval"('"public"."messages_id_seq"'::"regclass");



ALTER TABLE ONLY "public"."audit_logs"
    ADD CONSTRAINT "audit_logs_pkey" PRIMARY KEY ("id");



ALTER TABLE ONLY "public"."messages"
    ADD CONSTRAINT "messages_pkey" PRIMARY KEY ("id");



ALTER TABLE ONLY "public"."notifications"
    ADD CONSTRAINT "notifications_pkey" PRIMARY KEY ("id");



ALTER TABLE ONLY "public"."profiles"
    ADD CONSTRAINT "profiles_email_key" UNIQUE ("email");



ALTER TABLE ONLY "public"."profiles"
    ADD CONSTRAINT "profiles_pkey" PRIMARY KEY ("id");



ALTER TABLE ONLY "public"."reviews"
    ADD CONSTRAINT "reviews_pkey" PRIMARY KEY ("id");



ALTER TABLE ONLY "public"."task_gps_pings"
    ADD CONSTRAINT "task_gps_pings_pkey" PRIMARY KEY ("id");



ALTER TABLE ONLY "public"."tasks"
    ADD CONSTRAINT "tasks_pkey" PRIMARY KEY ("id");



ALTER TABLE ONLY "public"."timesheets"
    ADD CONSTRAINT "timesheets_pkey" PRIMARY KEY ("id");



ALTER TABLE ONLY "public"."users"
    ADD CONSTRAINT "users_email_key" UNIQUE ("email");



ALTER TABLE ONLY "public"."users"
    ADD CONSTRAINT "users_google_sub_key" UNIQUE ("google_sub");



ALTER TABLE ONLY "public"."users"
    ADD CONSTRAINT "users_pkey" PRIMARY KEY ("id");



ALTER TABLE ONLY "public"."users"
    ADD CONSTRAINT "users_supabase_sub_key" UNIQUE ("supabase_sub");



ALTER TABLE ONLY "public"."worklogs"
    ADD CONSTRAINT "worklogs_pkey" PRIMARY KEY ("id");



CREATE INDEX "idx_messages_task_created" ON "public"."messages" USING "btree" ("task_id", "created_at");



CREATE INDEX "idx_notifications_job" ON "public"."notifications" USING "btree" ("task_id");



CREATE INDEX "idx_notifications_user" ON "public"."notifications" USING "btree" ("user_id", "created_at" DESC);



CREATE INDEX "idx_notifications_user_created" ON "public"."notifications" USING "btree" ("user_id", "created_at" DESC);



CREATE INDEX "idx_notifications_user_unread" ON "public"."notifications" USING "btree" ("user_id", "unread");



CREATE INDEX "idx_tasks_assigned_to" ON "public"."tasks" USING "btree" ("assigned_to");



CREATE INDEX "idx_tasks_assigned_to_id" ON "public"."tasks" USING "btree" ("assigned_to_id");



CREATE INDEX "idx_tasks_assignee_status_created" ON "public"."tasks" USING "btree" ("assigned_to_id", "status", "created_at" DESC);



CREATE INDEX "idx_tasks_cancelled_at" ON "public"."tasks" USING "btree" ("cancelled_at");



CREATE INDEX "idx_tasks_requester" ON "public"."tasks" USING "btree" ("requester");



CREATE INDEX "idx_tasks_requester_id" ON "public"."tasks" USING "btree" ("requester_id");



CREATE INDEX "idx_tasks_requester_status_created" ON "public"."tasks" USING "btree" ("requester_id", "status", "created_at" DESC);



CREATE INDEX "idx_worklogs_task" ON "public"."worklogs" USING "btree" ("task_id");



CREATE INDEX "notifications_user_created_idx" ON "public"."notifications" USING "btree" ("user_id", "created_at" DESC);



CREATE INDEX "notifications_user_unread_created_idx" ON "public"."notifications" USING "btree" ("user_id", "unread", "created_at" DESC);



CREATE INDEX "task_gps_pings_task_id_created_at_idx" ON "public"."task_gps_pings" USING "btree" ("task_id", "created_at" DESC);



CREATE INDEX "tasks_assignee_done_created_id_idx" ON "public"."tasks" USING "btree" ("assigned_to_id", "created_at" DESC, "id" DESC) WHERE ("status" = 'completed'::"text");



CREATE INDEX "tasks_assignee_open_created_id_idx" ON "public"."tasks" USING "btree" ("assigned_to_id", "created_at" DESC, "id" DESC) WHERE ("status" = 'open'::"text");



CREATE INDEX "tasks_open_unassigned_created_id_idx" ON "public"."tasks" USING "btree" ("created_at" DESC, "id" DESC) WHERE (("status" = 'open'::"text") AND ("assigned_to_id" IS NULL));



CREATE INDEX "tasks_requester_closed_created_id_idx" ON "public"."tasks" USING "btree" ("requester_id", "created_at" DESC, "id" DESC) WHERE ("status" = ANY (ARRAY['completed'::"text", 'cancelled'::"text"]));



CREATE INDEX "tasks_requester_open_created_id_idx" ON "public"."tasks" USING "btree" ("requester_id", "created_at" DESC, "id" DESC) WHERE ("status" = 'open'::"text");



CREATE UNIQUE INDEX "users_email_lower_uidx" ON "public"."users" USING "btree" ("lower"("email"));



CREATE UNIQUE INDEX "users_email_lower_uniq" ON "public"."users" USING "btree" ("lower"("email")) WHERE (("email" IS NOT NULL) AND ("email" <> ''::"text"));



CREATE UNIQUE INDEX "users_google_sub_uniq" ON "public"."users" USING "btree" ("google_sub") WHERE ("google_sub" IS NOT NULL);



CREATE UNIQUE INDEX "users_supabase_sub_uniq" ON "public"."users" USING "btree" ("supabase_sub") WHERE ("supabase_sub" IS NOT NULL);



CREATE INDEX "worklogs_task_end_idx" ON "public"."worklogs" USING "btree" ("task_id", "end_at") WHERE ("end_at" IS NOT NULL);



CREATE INDEX "worklogs_task_open_idx" ON "public"."worklogs" USING "btree" ("task_id") WHERE ("end_at" IS NULL);



CREATE INDEX "worklogs_task_start_idx" ON "public"."worklogs" USING "btree" ("task_id", "start_at");



CREATE OR REPLACE TRIGGER " notify-new-task" AFTER INSERT ON "public"."tasks" FOR EACH ROW EXECUTE FUNCTION "supabase_functions"."http_request"('https://akxsdkerudurzcemurrb.supabase.co/functions/v1/notify-new-task', 'POST', '{"Content-type":"application/json"}', '{}', '5000');



CREATE OR REPLACE TRIGGER "on_task_inserted" AFTER INSERT ON "public"."tasks" FOR EACH ROW EXECUTE FUNCTION "public"."notify_new_task"();



ALTER TABLE ONLY "public"."messages"
    ADD CONSTRAINT "messages_sender_fkey" FOREIGN KEY ("sender") REFERENCES "auth"."users"("id");



ALTER TABLE ONLY "public"."profiles"
    ADD CONSTRAINT "profiles_user_fk" FOREIGN KEY ("id") REFERENCES "public"."users"("id") ON UPDATE CASCADE ON DELETE CASCADE;



ALTER TABLE ONLY "public"."reviews"
    ADD CONSTRAINT "reviews_reviewer_id_fkey" FOREIGN KEY ("reviewer_id") REFERENCES "public"."users"("id");



ALTER TABLE ONLY "public"."reviews"
    ADD CONSTRAINT "reviews_supporter_id_fkey" FOREIGN KEY ("supporter_id") REFERENCES "public"."users"("id");



ALTER TABLE ONLY "public"."reviews"
    ADD CONSTRAINT "reviews_task_id_fkey" FOREIGN KEY ("task_id") REFERENCES "public"."tasks"("id");



ALTER TABLE ONLY "public"."task_gps_pings"
    ADD CONSTRAINT "task_gps_pings_task_id_fkey" FOREIGN KEY ("task_id") REFERENCES "public"."tasks"("id") ON DELETE CASCADE;



ALTER TABLE ONLY "public"."tasks"
    ADD CONSTRAINT "tasks_requester_fk" FOREIGN KEY ("requester_id") REFERENCES "public"."users"("id") ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;



ALTER TABLE ONLY "public"."timesheets"
    ADD CONSTRAINT "timesheets_task_id_fkey" FOREIGN KEY ("task_id") REFERENCES "public"."tasks"("id");



ALTER TABLE ONLY "public"."timesheets"
    ADD CONSTRAINT "timesheets_worker_id_fkey" FOREIGN KEY ("worker_id") REFERENCES "public"."users"("id");



ALTER TABLE ONLY "public"."worklogs"
    ADD CONSTRAINT "worklogs_task_id_fkey" FOREIGN KEY ("task_id") REFERENCES "public"."tasks"("id") ON DELETE CASCADE;



ALTER TABLE "public"."audit_logs" ENABLE ROW LEVEL SECURITY;


CREATE POLICY "deny all (client)" ON "public"."audit_logs" TO "authenticated", "anon" USING (false) WITH CHECK (false);



CREATE POLICY "deny all (client)" ON "public"."notifications" TO "authenticated", "anon" USING (false) WITH CHECK (false);



CREATE POLICY "deny all (client)" ON "public"."users" TO "authenticated", "anon" USING (false) WITH CHECK (false);



ALTER TABLE "public"."messages" ENABLE ROW LEVEL SECURITY;


CREATE POLICY "msg_write" ON "public"."messages" FOR INSERT WITH CHECK (("auth"."uid"() = "sender"));



ALTER TABLE "public"."notifications" ENABLE ROW LEVEL SECURITY;


ALTER TABLE "public"."profiles" ENABLE ROW LEVEL SECURITY;


CREATE POLICY "profiles_read_own" ON "public"."profiles" FOR SELECT TO "authenticated" USING (("email" = ("auth"."jwt"() ->> 'email'::"text")));



CREATE POLICY "profiles_update_own" ON "public"."profiles" FOR UPDATE TO "authenticated" USING (("email" = ("auth"."jwt"() ->> 'email'::"text")));



ALTER TABLE "public"."reviews" ENABLE ROW LEVEL SECURITY;


ALTER TABLE "public"."task_gps_pings" ENABLE ROW LEVEL SECURITY;


ALTER TABLE "public"."tasks" ENABLE ROW LEVEL SECURITY;


CREATE POLICY "tasks_insert_self" ON "public"."tasks" FOR INSERT TO "authenticated" WITH CHECK (("requester_id" = "public"."current_user_id"()));



CREATE POLICY "tasks_read_own" ON "public"."tasks" FOR SELECT TO "authenticated" USING ((("requester_id" = "public"."current_user_id"()) OR ("assigned_to_id" = "public"."current_user_id"())));



CREATE POLICY "tasks_update_by_requester" ON "public"."tasks" FOR UPDATE TO "authenticated" USING (("requester_id" = "public"."current_user_id"()));



ALTER TABLE "public"."timesheets" ENABLE ROW LEVEL SECURITY;


ALTER TABLE "public"."users" ENABLE ROW LEVEL SECURITY;


ALTER TABLE "public"."worklogs" ENABLE ROW LEVEL SECURITY;


CREATE POLICY "worklogs_insert" ON "public"."worklogs" FOR INSERT TO "authenticated" WITH CHECK ((EXISTS ( SELECT 1
   FROM "public"."tasks" "t"
  WHERE (("t"."id" = "worklogs"."task_id") AND ("t"."assigned_to_id" = "public"."current_user_id"())))));



CREATE POLICY "worklogs_read" ON "public"."worklogs" FOR SELECT TO "authenticated" USING ((EXISTS ( SELECT 1
   FROM "public"."tasks" "t"
  WHERE (("t"."id" = "worklogs"."task_id") AND (("t"."assigned_to_id" = "public"."current_user_id"()) OR ("t"."requester_id" = "public"."current_user_id"()))))));



CREATE POLICY "worklogs_update" ON "public"."worklogs" FOR UPDATE TO "authenticated" USING ((EXISTS ( SELECT 1
   FROM "public"."tasks" "t"
  WHERE (("t"."id" = "worklogs"."task_id") AND ("t"."assigned_to_id" = "public"."current_user_id"())))));





ALTER PUBLICATION "supabase_realtime" OWNER TO "postgres";


ALTER PUBLICATION "supabase_realtime" ADD TABLE ONLY "public"."messages";






GRANT USAGE ON SCHEMA "public" TO "postgres";
GRANT USAGE ON SCHEMA "public" TO "anon";
GRANT USAGE ON SCHEMA "public" TO "authenticated";
GRANT USAGE ON SCHEMA "public" TO "service_role";















































































































































































































GRANT ALL ON FUNCTION "public"."adjust_time"("p_task_id" "uuid", "p_minutes_delta" integer) TO "anon";
GRANT ALL ON FUNCTION "public"."adjust_time"("p_task_id" "uuid", "p_minutes_delta" integer) TO "authenticated";
GRANT ALL ON FUNCTION "public"."adjust_time"("p_task_id" "uuid", "p_minutes_delta" integer) TO "service_role";



GRANT ALL ON FUNCTION "public"."assert_ops_admin"() TO "anon";
GRANT ALL ON FUNCTION "public"."assert_ops_admin"() TO "authenticated";
GRANT ALL ON FUNCTION "public"."assert_ops_admin"() TO "service_role";



GRANT ALL ON FUNCTION "public"."cancel_task"("p_task_id" "uuid", "p_reason" "text") TO "anon";
GRANT ALL ON FUNCTION "public"."cancel_task"("p_task_id" "uuid", "p_reason" "text") TO "authenticated";
GRANT ALL ON FUNCTION "public"."cancel_task"("p_task_id" "uuid", "p_reason" "text") TO "service_role";



GRANT ALL ON FUNCTION "public"."current_user_id"() TO "anon";
GRANT ALL ON FUNCTION "public"."current_user_id"() TO "authenticated";
GRANT ALL ON FUNCTION "public"."current_user_id"() TO "service_role";



GRANT ALL ON FUNCTION "public"."force_complete"("p_task_id" "uuid") TO "anon";
GRANT ALL ON FUNCTION "public"."force_complete"("p_task_id" "uuid") TO "authenticated";
GRANT ALL ON FUNCTION "public"."force_complete"("p_task_id" "uuid") TO "service_role";



GRANT ALL ON TABLE "public"."tasks" TO "anon";
GRANT ALL ON TABLE "public"."tasks" TO "authenticated";
GRANT ALL ON TABLE "public"."tasks" TO "service_role";



GRANT ALL ON TABLE "public"."users" TO "service_role";



GRANT ALL ON TABLE "public"."worklogs" TO "anon";
GRANT ALL ON TABLE "public"."worklogs" TO "authenticated";
GRANT ALL ON TABLE "public"."worklogs" TO "service_role";



GRANT ALL ON TABLE "public"."view_ops_task_worklog_agg" TO "service_role";



GRANT ALL ON TABLE "public"."view_ops_tasks" TO "service_role";



GRANT ALL ON FUNCTION "public"."get_ops_feed"("p_status" "text", "p_q" "text") TO "anon";
GRANT ALL ON FUNCTION "public"."get_ops_feed"("p_status" "text", "p_q" "text") TO "authenticated";
GRANT ALL ON FUNCTION "public"."get_ops_feed"("p_status" "text", "p_q" "text") TO "service_role";



GRANT ALL ON FUNCTION "public"."handle_new_user"() TO "anon";
GRANT ALL ON FUNCTION "public"."handle_new_user"() TO "authenticated";
GRANT ALL ON FUNCTION "public"."handle_new_user"() TO "service_role";



GRANT ALL ON FUNCTION "public"."is_ops_admin"() TO "anon";
GRANT ALL ON FUNCTION "public"."is_ops_admin"() TO "authenticated";
GRANT ALL ON FUNCTION "public"."is_ops_admin"() TO "service_role";



GRANT ALL ON FUNCTION "public"."notify_new_task"() TO "anon";
GRANT ALL ON FUNCTION "public"."notify_new_task"() TO "authenticated";
GRANT ALL ON FUNCTION "public"."notify_new_task"() TO "service_role";



GRANT ALL ON FUNCTION "public"."whoami"() TO "anon";
GRANT ALL ON FUNCTION "public"."whoami"() TO "authenticated";
GRANT ALL ON FUNCTION "public"."whoami"() TO "service_role";


















GRANT ALL ON TABLE "public"."audit_logs" TO "service_role";



GRANT ALL ON TABLE "public"."messages" TO "anon";
GRANT ALL ON TABLE "public"."messages" TO "authenticated";
GRANT ALL ON TABLE "public"."messages" TO "service_role";



GRANT ALL ON SEQUENCE "public"."messages_id_seq" TO "anon";
GRANT ALL ON SEQUENCE "public"."messages_id_seq" TO "authenticated";
GRANT ALL ON SEQUENCE "public"."messages_id_seq" TO "service_role";



GRANT ALL ON TABLE "public"."notifications" TO "service_role";



GRANT ALL ON TABLE "public"."profiles" TO "anon";
GRANT ALL ON TABLE "public"."profiles" TO "authenticated";
GRANT ALL ON TABLE "public"."profiles" TO "service_role";



GRANT ALL ON TABLE "public"."reviews" TO "anon";
GRANT ALL ON TABLE "public"."reviews" TO "authenticated";
GRANT ALL ON TABLE "public"."reviews" TO "service_role";



GRANT ALL ON TABLE "public"."task_gps_pings" TO "anon";
GRANT ALL ON TABLE "public"."task_gps_pings" TO "authenticated";
GRANT ALL ON TABLE "public"."task_gps_pings" TO "service_role";



GRANT ALL ON TABLE "public"."timesheets" TO "anon";
GRANT ALL ON TABLE "public"."timesheets" TO "authenticated";
GRANT ALL ON TABLE "public"."timesheets" TO "service_role";









ALTER DEFAULT PRIVILEGES FOR ROLE "postgres" IN SCHEMA "public" GRANT ALL ON SEQUENCES TO "postgres";
ALTER DEFAULT PRIVILEGES FOR ROLE "postgres" IN SCHEMA "public" GRANT ALL ON SEQUENCES TO "anon";
ALTER DEFAULT PRIVILEGES FOR ROLE "postgres" IN SCHEMA "public" GRANT ALL ON SEQUENCES TO "authenticated";
ALTER DEFAULT PRIVILEGES FOR ROLE "postgres" IN SCHEMA "public" GRANT ALL ON SEQUENCES TO "service_role";






ALTER DEFAULT PRIVILEGES FOR ROLE "postgres" IN SCHEMA "public" GRANT ALL ON FUNCTIONS TO "postgres";
ALTER DEFAULT PRIVILEGES FOR ROLE "postgres" IN SCHEMA "public" GRANT ALL ON FUNCTIONS TO "anon";
ALTER DEFAULT PRIVILEGES FOR ROLE "postgres" IN SCHEMA "public" GRANT ALL ON FUNCTIONS TO "authenticated";
ALTER DEFAULT PRIVILEGES FOR ROLE "postgres" IN SCHEMA "public" GRANT ALL ON FUNCTIONS TO "service_role";






ALTER DEFAULT PRIVILEGES FOR ROLE "postgres" IN SCHEMA "public" GRANT ALL ON TABLES TO "postgres";
ALTER DEFAULT PRIVILEGES FOR ROLE "postgres" IN SCHEMA "public" GRANT ALL ON TABLES TO "anon";
ALTER DEFAULT PRIVILEGES FOR ROLE "postgres" IN SCHEMA "public" GRANT ALL ON TABLES TO "authenticated";
ALTER DEFAULT PRIVILEGES FOR ROLE "postgres" IN SCHEMA "public" GRANT ALL ON TABLES TO "service_role";































drop trigger if exists "on_task_inserted" on "public"."tasks";

drop policy "deny all (client)" on "public"."audit_logs";

drop policy "deny all (client)" on "public"."notifications";

drop policy "tasks_insert_self" on "public"."tasks";

drop policy "tasks_read_own" on "public"."tasks";

drop policy "tasks_update_by_requester" on "public"."tasks";

drop policy "deny all (client)" on "public"."users";

drop policy "worklogs_insert" on "public"."worklogs";

drop policy "worklogs_read" on "public"."worklogs";

drop policy "worklogs_update" on "public"."worklogs";

revoke references on table "public"."audit_logs" from "anon";

revoke trigger on table "public"."audit_logs" from "anon";

revoke truncate on table "public"."audit_logs" from "anon";

revoke references on table "public"."audit_logs" from "authenticated";

revoke trigger on table "public"."audit_logs" from "authenticated";

revoke truncate on table "public"."audit_logs" from "authenticated";

revoke references on table "public"."notifications" from "anon";

revoke trigger on table "public"."notifications" from "anon";

revoke truncate on table "public"."notifications" from "anon";

revoke references on table "public"."notifications" from "authenticated";

revoke trigger on table "public"."notifications" from "authenticated";

revoke truncate on table "public"."notifications" from "authenticated";

revoke references on table "public"."users" from "anon";

revoke trigger on table "public"."users" from "anon";

revoke truncate on table "public"."users" from "anon";

revoke references on table "public"."users" from "authenticated";

revoke trigger on table "public"."users" from "authenticated";

revoke truncate on table "public"."users" from "authenticated";

alter table "public"."profiles" drop constraint "profiles_user_fk";

alter table "public"."reviews" drop constraint "reviews_reviewer_id_fkey";

alter table "public"."reviews" drop constraint "reviews_supporter_id_fkey";

alter table "public"."reviews" drop constraint "reviews_task_id_fkey";

alter table "public"."task_gps_pings" drop constraint "task_gps_pings_task_id_fkey";

alter table "public"."tasks" drop constraint "tasks_requester_fk";

alter table "public"."timesheets" drop constraint "timesheets_task_id_fkey";

alter table "public"."timesheets" drop constraint "timesheets_worker_id_fkey";

alter table "public"."worklogs" drop constraint "worklogs_task_id_fkey";

alter table "public"."messages" alter column "id" set default nextval('public.messages_id_seq'::regclass);

alter table "public"."notifications" alter column "type" set data type public.notification_type using "type"::text::public.notification_type;

alter table "public"."profiles" add constraint "profiles_user_fk" FOREIGN KEY (id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE not valid;

alter table "public"."profiles" validate constraint "profiles_user_fk";

alter table "public"."reviews" add constraint "reviews_reviewer_id_fkey" FOREIGN KEY (reviewer_id) REFERENCES public.users(id) not valid;

alter table "public"."reviews" validate constraint "reviews_reviewer_id_fkey";

alter table "public"."reviews" add constraint "reviews_supporter_id_fkey" FOREIGN KEY (supporter_id) REFERENCES public.users(id) not valid;

alter table "public"."reviews" validate constraint "reviews_supporter_id_fkey";

alter table "public"."reviews" add constraint "reviews_task_id_fkey" FOREIGN KEY (task_id) REFERENCES public.tasks(id) not valid;

alter table "public"."reviews" validate constraint "reviews_task_id_fkey";

alter table "public"."task_gps_pings" add constraint "task_gps_pings_task_id_fkey" FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE not valid;

alter table "public"."task_gps_pings" validate constraint "task_gps_pings_task_id_fkey";

alter table "public"."tasks" add constraint "tasks_requester_fk" FOREIGN KEY (requester_id) REFERENCES public.users(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED not valid;

alter table "public"."tasks" validate constraint "tasks_requester_fk";

alter table "public"."timesheets" add constraint "timesheets_task_id_fkey" FOREIGN KEY (task_id) REFERENCES public.tasks(id) not valid;

alter table "public"."timesheets" validate constraint "timesheets_task_id_fkey";

alter table "public"."timesheets" add constraint "timesheets_worker_id_fkey" FOREIGN KEY (worker_id) REFERENCES public.users(id) not valid;

alter table "public"."timesheets" validate constraint "timesheets_worker_id_fkey";

alter table "public"."worklogs" add constraint "worklogs_task_id_fkey" FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE not valid;

alter table "public"."worklogs" validate constraint "worklogs_task_id_fkey";

set check_function_bodies = off;

CREATE OR REPLACE FUNCTION public.get_ops_feed(p_status text DEFAULT NULL::text, p_q text DEFAULT NULL::text)
 RETURNS SETOF public.view_ops_tasks
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public'
AS $function$
begin
  perform public.assert_ops_admin();

  return query
  select v.*
  from public.view_ops_tasks v
  where (p_status is null or p_status = 'all' or v.status = p_status)
    and (
      p_q is null
      or p_q = ''
      or (v.title ilike '%'||p_q||'%'
          or v.location_text ilike '%'||p_q||'%'
          or v.requester_email ilike '%'||p_q||'%'
          or v.supporter_email ilike '%'||p_q||'%')
    )
  order by v.last_event_at desc
  limit 500;
end$function$
;

create or replace view "public"."view_ops_task_worklog_agg" as  SELECT task_id,
    min(start_at) AS first_start_at,
    max(end_at) AS last_end_at,
    COALESCE(sum(
        CASE
            WHEN (end_at IS NOT NULL) THEN ((EXTRACT(epoch FROM (end_at - start_at)))::integer / 60)
            ELSE 0
        END), (0)::bigint) AS total_minutes_done,
    COALESCE(sum(
        CASE
            WHEN (end_at IS NULL) THEN ((EXTRACT(epoch FROM (now() - start_at)))::integer / 60)
            ELSE 0
        END), (0)::bigint) AS running_minutes,
    max(COALESCE(end_at, start_at)) AS last_wl_event_at
   FROM public.worklogs w
  GROUP BY task_id;


create or replace view "public"."view_ops_tasks" as  SELECT t.id AS task_id,
    t.title,
    t.category,
    t.location_text,
    t.status,
    t.estimated_minutes,
    ((t.prepay_amount_cents)::numeric / 100.0) AS prepay_amount,
    t.is_immediate,
    t.scheduled_at,
    t.created_at,
    t.cancelled_at,
    t.cancel_reason,
    r.email AS requester_email,
    s.email AS supporter_email,
    a.first_start_at,
    a.last_end_at,
    a.total_minutes_done,
    a.running_minutes,
    (COALESCE(a.total_minutes_done, (0)::bigint) + COALESCE(a.running_minutes, (0)::bigint)) AS duration_minutes,
    GREATEST(t.created_at, COALESCE(a.last_wl_event_at, t.created_at), COALESCE(t.cancelled_at, t.created_at)) AS last_event_at
   FROM (((public.tasks t
     LEFT JOIN public.users r ON ((r.id = t.requester_id)))
     LEFT JOIN public.users s ON ((s.id = t.assigned_to_id)))
     LEFT JOIN public.view_ops_task_worklog_agg a ON ((a.task_id = t.id)));



  create policy "deny all (client)"
  on "public"."audit_logs"
  as permissive
  for all
  to anon, authenticated
using (false)
with check (false);



  create policy "deny all (client)"
  on "public"."notifications"
  as permissive
  for all
  to anon, authenticated
using (false)
with check (false);



  create policy "tasks_insert_self"
  on "public"."tasks"
  as permissive
  for insert
  to authenticated
with check ((requester_id = public.current_user_id()));



  create policy "tasks_read_own"
  on "public"."tasks"
  as permissive
  for select
  to authenticated
using (((requester_id = public.current_user_id()) OR (assigned_to_id = public.current_user_id())));



  create policy "tasks_update_by_requester"
  on "public"."tasks"
  as permissive
  for update
  to authenticated
using ((requester_id = public.current_user_id()));



  create policy "deny all (client)"
  on "public"."users"
  as permissive
  for all
  to anon, authenticated
using (false)
with check (false);



  create policy "worklogs_insert"
  on "public"."worklogs"
  as permissive
  for insert
  to authenticated
with check ((EXISTS ( SELECT 1
   FROM public.tasks t
  WHERE ((t.id = worklogs.task_id) AND (t.assigned_to_id = public.current_user_id())))));



  create policy "worklogs_read"
  on "public"."worklogs"
  as permissive
  for select
  to authenticated
using ((EXISTS ( SELECT 1
   FROM public.tasks t
  WHERE ((t.id = worklogs.task_id) AND ((t.assigned_to_id = public.current_user_id()) OR (t.requester_id = public.current_user_id()))))));



  create policy "worklogs_update"
  on "public"."worklogs"
  as permissive
  for update
  to authenticated
using ((EXISTS ( SELECT 1
   FROM public.tasks t
  WHERE ((t.id = worklogs.task_id) AND (t.assigned_to_id = public.current_user_id())))));


CREATE TRIGGER on_task_inserted AFTER INSERT ON public.tasks FOR EACH ROW EXECUTE FUNCTION public.notify_new_task();


  create policy "Allow authenticated uploads"
  on "storage"."objects"
  as permissive
  for insert
  to authenticated
with check ((bucket_id = 'task-completions'::text));



  create policy "Allow public read"
  on "storage"."objects"
  as permissive
  for select
  to public
using ((bucket_id = 'task-completions'::text));



  create policy "Auth can delete avatars"
  on "storage"."objects"
  as permissive
  for delete
  to authenticated
using ((bucket_id = 'avatars'::text));



  create policy "Auth can insert avatars"
  on "storage"."objects"
  as permissive
  for insert
  to authenticated
with check ((bucket_id = 'avatars'::text));



  create policy "Auth can update avatars"
  on "storage"."objects"
  as permissive
  for update
  to authenticated
using ((bucket_id = 'avatars'::text))
with check ((bucket_id = 'avatars'::text));



  create policy "Public read avatars"
  on "storage"."objects"
  as permissive
  for select
  to public
using ((bucket_id = 'avatars'::text));



